package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App es el puente entre Go y la ventana. Wails expone al JavaScript todos
// los métodos EXPORTADOS (mayúscula inicial) de este struct como funciones
// asíncronas: window.go.main.App.Submit(...) devuelve una Promise.
//
// Los parámetros y resultados viajan como JSON, así que tienen que ser
// tipos serializables (structs con campos exportados, strings, números...).
// Un `error` devuelto se convierte en un rechazo de la Promise.
type App struct {
	ctx      context.Context
	emit     func(event string, data ...any) // manda un evento a la ventana
	store    *jobStore
	mu       sync.Mutex // protege settings, tools y libRoot
	settings settings
	libRoot  string // carpeta que se está mirando en la Biblioteca (ListAlbums)
	tools    toolsState
	batch    batchState   // Biblioteca en lote (batch.go)
	icons    iconRun      // lote de iconos de carpeta (icons_folder.go)
	lengths  lengthsCache // duraciones tomadas de otra fuente (lengths.go)
}

// toolsState es lo que la ventana muestra en la barra de herramientas:
// si están listas, si se están descargando (y cuánto falta) o si falló.
type toolsState struct {
	State    string       `json:"state"` // checking | downloading | ready | error
	Message  string       `json:"message"`
	Progress float64      `json:"progress"` // 0-100 mientras descarga
	Tools    []toolStatus `json:"tools"`
}

// appState es todo lo que la ventana necesita al arrancar.
type appState struct {
	Settings settings   `json:"settings"`
	Tools    toolsState `json:"tools"`
	BinDir   string     `json:"binDir"`
	Version  string     `json:"version"` // la de la release (-ldflags), o "dev"
}

// fileInfo describe un fichero descargado para la lista de recientes.
type fileInfo struct {
	Path string    `json:"path"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
}

func NewApp() *App {
	// Hasta que Wails llame a startup no hay ventana: los eventos se ignoran.
	return &App{ctx: context.Background(), emit: func(string, ...any) {}}
}

// startup lo llama Wails cuando la ventana ya existe. El ctx que recibe es
// el que hay que usar para hablar con la ventana (eventos, diálogos...).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.emit = func(event string, data ...any) { runtime.EventsEmit(ctx, event, data...) }
	a.settings = loadSettings()
	a.store = newJobStore(func() { a.emit("jobs") })
	a.store.onFinish = a.afterDownload
	go a.prepareTools()
}

// shutdown: cancelar descargas en curso y parar slskd para no dejar
// procesos huérfanos.
func (a *App) shutdown(ctx context.Context) {
	a.store.cancelAll()
	slsk.stop()
	cardDisk.flush() // tarjetas y miniaturas pendientes de guardar (player.go)
}

// prepareTools comprueba bin/ y descarga lo que falte, informando a la
// ventana en cada paso con el evento "tools".
func (a *App) prepareTools() {
	a.setTools(toolsState{State: "checking", Message: "Comprobando herramientas..."})

	err := ensureTools(a.ctx, func(name string, done, total int64) {
		pct := 0.0
		if total > 0 {
			pct = float64(done) * 100 / float64(total)
		}
		a.setTools(toolsState{
			State:    "downloading",
			Message:  fmt.Sprintf("Descargando %s (%s)", name, humanSize(done)),
			Progress: pct,
		})
	})
	if err != nil {
		a.setTools(toolsState{State: "error", Message: err.Error(), Tools: toolStatuses()})
		return
	}

	st := toolStatuses()
	a.setTools(toolsState{State: "ready", Message: "Listo", Tools: st})
}

func (a *App) setTools(t toolsState) {
	a.mu.Lock()
	a.tools = t
	a.mu.Unlock()
	a.emit("tools", t)
}

// ---- Métodos expuestos a la ventana --------------------------------------

// State devuelve la configuración y el estado de las herramientas.
func (a *App) State() appState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return appState{Settings: a.settings, Tools: a.tools, BinDir: binDir(), Version: version}
}

// SaveSettings guarda los ajustes de la pestaña Descargas y devuelve los
// ajustes completos tal como quedan. De lo que manda la ventana solo se toman
// esos campos: la colección y los de Soulseek los guardan sus propias
// pestañas, y la copia que tiene la ventana puede ser vieja (si no, elegir la
// colección y luego cambiar el formato devolvería la colección anterior).
func (a *App) SaveSettings(in settings) (settings, error) {
	if strings.TrimSpace(in.OutDir) == "" {
		return settings{}, fmt.Errorf("la carpeta de destino no puede estar vacía")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.settings
	s.OutDir, s.Format, s.Quality = in.OutDir, in.Format, in.Quality
	s.DiscogsToken, s.FolderIcons = in.DiscogsToken, in.FolderIcons
	s.Workers = min(max(in.Workers, 1), 8)
	if err := s.save(); err != nil {
		return settings{}, err
	}
	a.settings = s
	return s, nil
}

// ChooseFolder abre el diálogo nativo de elegir carpeta. Devuelve "" si el
// usuario cancela.
func (a *App) ChooseFolder() (string, error) {
	a.mu.Lock()
	current := a.settings.OutDir
	a.mu.Unlock()
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Carpeta de descargas",
		DefaultDirectory: existingDir(current),
	})
}

// OpenFolder abre la carpeta de descargas en el Explorador.
func (a *App) OpenFolder() error {
	a.mu.Lock()
	dir := a.settings.OutDir
	a.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return openDir(dir)
}

// OpenPath abre cualquier carpeta en el Explorador.
func (a *App) OpenPath(dir string) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("no existe la carpeta %s", dir)
	}
	return openDir(dir)
}

// Submit recibe el texto pegado y arranca un trabajo. Devuelve su id.
func (a *App) Submit(text, format, quality string) (int, error) {
	urls := parseURLText(text)
	if len(urls) == 0 {
		return 0, fmt.Errorf("no hay enlaces válidos (tienen que empezar por http:// o https://)")
	}

	a.mu.Lock()
	if a.tools.State != "ready" {
		a.mu.Unlock()
		return 0, fmt.Errorf("las herramientas aún no están listas")
	}
	cfg := config{
		outDir:  a.settings.OutDir,
		workers: a.settings.Workers,
		format:  a.settings.Format,
		quality: a.settings.Quality,
	}
	a.mu.Unlock()

	if format != "" {
		cfg.format = format
	}
	if quality != "" {
		cfg.quality = quality
	}
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		return 0, fmt.Errorf("no puedo crear %s: %w", cfg.outDir, err)
	}
	return a.store.add(urls, cfg).ID, nil
}

// Jobs devuelve todos los trabajos, el más reciente primero.
func (a *App) Jobs() []job {
	return a.store.snapshot()
}

// Cancel detiene un trabajo en marcha.
func (a *App) Cancel(id int) bool {
	return a.store.cancelJob(id)
}

// Files lista los ficheros de audio de la carpeta de descargas, los más
// recientes primero.
func (a *App) Files() ([]fileInfo, error) {
	a.mu.Lock()
	root := a.settings.OutDir
	a.mu.Unlock()

	var files []fileInfo
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".mp3", ".m4a", ".opus", ".flac", ".wav", ".ogg":
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			files = append(files, fileInfo{Path: filepath.ToSlash(rel), Size: info.Size(), Mod: info.ModTime()})
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(files, func(i, k int) bool { return files[i].Mod.After(files[k].Mod) })
	if len(files) > 60 {
		files = files[:60]
	}
	return files, nil
}

// UpdateYtdlp ejecuta el autoactualizador de yt-dlp y refresca versiones.
func (a *App) UpdateYtdlp() (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	msg, err := updateYtdlp(ctx)
	a.setTools(toolsState{State: "ready", Message: "Listo", Tools: toolStatuses()})
	return msg, err
}

// RetryTools vuelve a intentar la descarga de herramientas tras un error.
func (a *App) RetryTools() {
	go a.prepareTools()
}

// ---- Utilidades ----------------------------------------------------------

// parseURLText convierte el texto pegado (un enlace por línea; también
// admite separados por espacios) en una lista sin duplicados.
func parseURLText(text string) []string {
	var urls []string
	for _, tok := range strings.Fields(text) {
		if strings.HasPrefix(tok, "http://") || strings.HasPrefix(tok, "https://") {
			urls = append(urls, tok)
		}
	}
	return dedupe(urls)
}
