package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
	icons    iconRun      // lote de iconos de carpeta (folder_icons.go)
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

// SaveSettings guarda los ajustes que vienen de la ventana.
func (a *App) SaveSettings(s settings) error {
	if s.Workers < 1 {
		s.Workers = 1
	}
	if s.Workers > 8 {
		s.Workers = 8
	}
	if strings.TrimSpace(s.OutDir) == "" {
		return fmt.Errorf("la carpeta de destino no puede estar vacía")
	}
	if err := s.save(); err != nil {
		return err
	}
	a.mu.Lock()
	a.settings = s
	a.mu.Unlock()
	return nil
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

// existingDir devuelve la carpeta si existe o, si no, la primera de sus
// carpetas madre que exista. El diálogo nativo de Windows falla (sin abrirse)
// si se le pide arrancar en una ruta inexistente, y la carpeta de descargas
// no se crea hasta la primera descarga.
func existingDir(dir string) string {
	for dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// OpenFolder abre la carpeta de descargas en el Explorador.
func (a *App) OpenFolder() error {
	a.mu.Lock()
	dir := a.settings.OutDir
	a.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return exec.Command("explorer", dir).Start()
}

// OpenPath abre cualquier carpeta en el Explorador.
func (a *App) OpenPath(dir string) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("no existe la carpeta %s", dir)
	}
	return exec.Command("explorer", dir).Start()
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

// dedupe elimina duplicados conservando el orden. El truco del
// map[string]struct{} es el "set" idiomático de Go: struct{} ocupa 0 bytes.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, u := range in {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func humanSize(b int64) string {
	switch {
	case b > 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b > 1<<10:
		return fmt.Sprintf("%d kB", b>>10)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
