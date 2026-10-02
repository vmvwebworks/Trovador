package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Pestaña Soulseek. El protocolo lo lleva slskd (https://github.com/slskd/slskd),
// un demonio Soulseek de código abierto que la app descarga a bin\ cuando se
// usa por primera vez, arranca de fondo (sin interfaz propia, solo escuchando
// en 127.0.0.1) y maneja por su API HTTP: buscar, ver resultados, descargar y
// seguir las transferencias. Igual que con yt-dlp: el motor es de otros, la
// integración con la biblioteca es nuestra.

// slskState es lo que la pestaña muestra arriba.
type slskState struct {
	Installed bool   `json:"installed"` // slskd.exe en bin\
	Running   bool   `json:"running"`   // proceso arrancado y API respondiendo
	Connected bool   `json:"connected"` // logueado en la red Soulseek
	User      string `json:"user"`
	Message   string `json:"message"`
	Downloads string `json:"downloads"` // carpeta donde caen las descargas
	Share     string `json:"share"`
	Port      int    `json:"port"`
}

// ---- ajustes -------------------------------------------------------------

// slskSettings devuelve los ajustes de Soulseek con valores por defecto: la
// carpeta compartida es la madre de la de descargas (la biblioteca) si tiene
// sentido, y el puerto de escucha el habitual de Soulseek.
func (a *App) slskSettings() (user, pass, share, downloads string, port int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.settings
	share = s.SlskShare
	if share == "" {
		if parent := filepath.Dir(s.OutDir); parent != s.OutDir && filepath.Dir(parent) != parent {
			share = parent
		} else {
			share = s.OutDir
		}
	}
	port = s.SlskPort
	if port == 0 {
		port = 50300
	}
	return s.SlskUser, s.SlskPass, share, s.OutDir, port
}

// SoulseekState: para pintar la cabecera de la pestaña.
func (a *App) SoulseekState() slskState {
	user, _, share, downloads, port := a.slskSettings()
	st := slskState{Installed: fileExists(toolPath("slskd.exe")), User: user, Share: share, Downloads: downloads, Port: port}
	if !st.Installed {
		st.Message = "slskd no está instalado (60 MB)"
		return st
	}
	if !slsk.alive() {
		st.Message = "motor parado"
		return st
	}
	st.Running = true
	var srv struct {
		IsLoggedIn   bool   `json:"isLoggedIn"`
		IsConnecting bool   `json:"isConnecting"`
		State        string `json:"state"`
	}
	if err := slsk.api(a.ctx, http.MethodGet, "/api/v0/server", nil, &srv); err != nil {
		st.Message = "motor sin responder: " + err.Error()
		return st
	}
	st.Connected = srv.IsLoggedIn
	switch {
	case srv.IsLoggedIn:
		st.Message = "conectado como " + user
	case srv.IsConnecting:
		st.Message = "conectando..."
	default:
		st.Message = "desconectado (" + srv.State + ")"
	}
	return st
}

// SoulseekInstall descarga slskd a bin\ (60 MB), informando por el evento
// "tools" como las demás herramientas.
func (a *App) SoulseekInstall() error {
	for _, t := range toolDefs {
		if t.Name != "slskd" || !t.missing() {
			continue
		}
		a.libLog("descargando slskd...")
		err := fetchTool(a.ctx, t, func(name string, done, total int64) {
			pct := 0.0
			if total > 0 {
				pct = float64(done) * 100 / float64(total)
			}
			a.emit("slsk-progress", map[string]any{"name": name, "done": done, "total": total, "pct": pct})
		})
		if err != nil {
			return err
		}
		a.setTools(toolsState{State: "ready", Message: "Listo", Tools: toolStatuses()})
	}
	return nil
}

// SoulseekConnect arranca slskd (si no lo está) con los ajustes actuales y
// espera a que entre en la red.
func (a *App) SoulseekConnect() (slskState, error) {
	user, pass, share, downloads, port := a.slskSettings()
	if user == "" || pass == "" {
		return a.SoulseekState(), fmt.Errorf("pon tu usuario y contraseña de Soulseek en los ajustes de la pestaña")
	}
	if !fileExists(toolPath("slskd.exe")) {
		return a.SoulseekState(), fmt.Errorf("slskd no está instalado")
	}
	if err := slsk.start(a, user, pass, share, downloads, port); err != nil {
		return a.SoulseekState(), err
	}
	// Con credenciales cambiadas hay que reconectar; PUT /server conecta.
	if err := slsk.api(a.ctx, http.MethodPut, "/api/v0/server", nil, nil); err != nil {
		a.libLog("soulseek: %v", err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		st := a.SoulseekState()
		if st.Connected {
			return st, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	st := a.SoulseekState()
	return st, fmt.Errorf("no se ha podido entrar en Soulseek: %s (¿usuario/contraseña correctos?)", st.Message)
}

// SoulseekDisconnect para el motor.
func (a *App) SoulseekDisconnect() slskState {
	slsk.stop()
	return a.SoulseekState()
}

// SaveSoulseekSettings guarda credenciales, carpeta compartida y puerto. Si
// el motor está en marcha se para: al reconectar arranca con lo nuevo.
func (a *App) SaveSoulseekSettings(user, pass, share string, port int) error {
	a.mu.Lock()
	s := a.settings
	s.SlskUser, s.SlskPass, s.SlskShare = strings.TrimSpace(user), pass, strings.TrimSpace(share)
	if port > 0 {
		s.SlskPort = port
	}
	if err := s.save(); err != nil {
		a.mu.Unlock()
		return err
	}
	a.settings = s
	a.mu.Unlock()
	slsk.stop()
	return nil
}

// ChooseShareFolder abre el diálogo para elegir la carpeta compartida.
func (a *App) ChooseShareFolder() (string, error) {
	_, _, share, _, _ := a.slskSettings()
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Carpeta compartida en Soulseek", DefaultDirectory: existingDir(share)})
}

// ---- búsqueda -------------------------------------------------------------

// slskFile es un fichero ofrecido por un usuario.
type slskFile struct {
	Filename string  `json:"filename"` // ruta remota completa (es lo que hay que pedir)
	Name     string  `json:"name"`     // solo el nombre
	Folder   string  `json:"folder"`   // carpeta remota (último tramo)
	Size     int64   `json:"size"`
	BitRate  int     `json:"bitRate"`
	Length   float64 `json:"length"` // segundos
	Locked   bool    `json:"locked"`
}

// slskResponse es lo que ofrece un usuario para una búsqueda.
type slskResponse struct {
	Username    string     `json:"username"`
	FreeSlot    bool       `json:"freeSlot"`
	QueueLength int        `json:"queueLength"`
	UploadSpeed int64      `json:"uploadSpeed"` // bytes/s
	Files       []slskFile `json:"files"`
}

type slskSearch struct {
	ID        string         `json:"id"`
	Text      string         `json:"text"`
	State     string         `json:"state"`
	Complete  bool           `json:"complete"`
	Files     int            `json:"files"`
	Responses []slskResponse `json:"responses"`
}

// SoulseekSearch lanza una búsqueda y devuelve su id; los resultados se
// van pidiendo con SoulseekResults.
func (a *App) SoulseekSearch(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("escribe algo que buscar")
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := slsk.api(a.ctx, http.MethodPost, "/api/v0/searches", map[string]string{"searchText": text}, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

// SoulseekResults devuelve el estado y los resultados de una búsqueda,
// ordenados: usuarios con hueco libre y más velocidad primero.
func (a *App) SoulseekResults(id string) (slskSearch, error) {
	var meta struct {
		ID         string `json:"id"`
		SearchText string `json:"searchText"`
		State      string `json:"state"`
		IsComplete bool   `json:"isComplete"`
		FileCount  int    `json:"fileCount"`
	}
	if err := slsk.api(a.ctx, http.MethodGet, "/api/v0/searches/"+url.PathEscape(id), nil, &meta); err != nil {
		return slskSearch{}, err
	}
	var raw []struct {
		Username          string `json:"username"`
		HasFreeUploadSlot bool   `json:"hasFreeUploadSlot"`
		QueueLength       int    `json:"queueLength"`
		UploadSpeed       int64  `json:"uploadSpeed"`
		Files             []struct {
			Filename string  `json:"filename"`
			Size     int64   `json:"size"`
			BitRate  int     `json:"bitRate"`
			Length   float64 `json:"length"`
			IsLocked bool    `json:"isLocked"`
		} `json:"files"`
	}
	if err := slsk.api(a.ctx, http.MethodGet, "/api/v0/searches/"+url.PathEscape(id)+"/responses", nil, &raw); err != nil {
		return slskSearch{}, err
	}
	// Responses se inicializa vacío para que llegue como [] y no como null a la
	// ventana.
	out := slskSearch{ID: meta.ID, Text: meta.SearchText, State: meta.State, Complete: meta.IsComplete, Files: meta.FileCount, Responses: []slskResponse{}}
	for _, r := range raw {
		resp := slskResponse{Username: r.Username, FreeSlot: r.HasFreeUploadSlot, QueueLength: r.QueueLength, UploadSpeed: r.UploadSpeed, Files: []slskFile{}}
		for _, f := range r.Files {
			parts := strings.Split(f.Filename, `\`)
			name := parts[len(parts)-1]
			folder := ""
			if len(parts) >= 2 {
				folder = parts[len(parts)-2]
			}
			resp.Files = append(resp.Files, slskFile{Filename: f.Filename, Name: name, Folder: folder, Size: f.Size, BitRate: f.BitRate, Length: f.Length, Locked: f.IsLocked})
		}
		out.Responses = append(out.Responses, resp)
	}
	sortResponses(out.Responses)
	return out, nil
}

// sortResponses: con hueco libre primero, luego sin cola, luego más rápido.
func sortResponses(rs []slskResponse) {
	less := func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.FreeSlot != b.FreeSlot {
			return a.FreeSlot
		}
		if (a.QueueLength == 0) != (b.QueueLength == 0) {
			return a.QueueLength == 0
		}
		return a.UploadSpeed > b.UploadSpeed
	}
	for i := 1; i < len(rs); i++ { // inserción: pocas decenas de usuarios
		for j := i; j > 0 && less(j, j-1); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// ---- descargas ------------------------------------------------------------

// SoulseekDownload pide a un usuario los ficheros indicados (por ruta remota
// y tamaño, que es como los identifica el protocolo).
func (a *App) SoulseekDownload(username string, files []slskFile) (int, error) {
	if len(files) == 0 {
		return 0, fmt.Errorf("no hay ficheros que descargar")
	}
	var req []map[string]any
	for _, f := range files {
		req = append(req, map[string]any{"filename": f.Filename, "size": f.Size})
	}
	if err := slsk.api(a.ctx, http.MethodPost, "/api/v0/transfers/downloads/"+url.PathEscape(username), req, nil); err != nil {
		return 0, err
	}
	a.libLog("soulseek: pedidos %d fichero(s) a %s", len(files), username)
	return len(files), nil
}

// slskTransfer es una descarga en curso o terminada.
type slskTransfer struct {
	ID       string  `json:"id"`
	Username string  `json:"username"`
	Name     string  `json:"name"`
	Folder   string  `json:"folder"`
	Size     int64   `json:"size"`
	State    string  `json:"state"` // p. ej. "Queued, Remotely", "InProgress", "Completed, Succeeded"
	Percent  float64 `json:"percent"`
	Speed    float64 `json:"speed"` // bytes/s
	Error    string  `json:"error"`
}

// SoulseekTransfers lista las descargas, las más recientes primero.
func (a *App) SoulseekTransfers() ([]slskTransfer, error) {
	var raw []struct {
		Username    string `json:"username"`
		Directories []struct {
			Directory string `json:"directory"`
			Files     []struct {
				ID              string  `json:"id"`
				Filename        string  `json:"filename"`
				Size            int64   `json:"size"`
				State           string  `json:"state"`
				PercentComplete float64 `json:"percentComplete"`
				AverageSpeed    float64 `json:"averageSpeed"`
				Exception       string  `json:"exception"`
				RequestedAt     string  `json:"requestedAt"`
			} `json:"files"`
		} `json:"directories"`
	}
	if err := slsk.api(a.ctx, http.MethodGet, "/api/v0/transfers/downloads", nil, &raw); err != nil {
		return nil, err
	}
	out := []slskTransfer{}
	for _, u := range raw {
		for _, d := range u.Directories {
			for _, f := range d.Files {
				parts := strings.Split(f.Filename, `\`)
				errMsg := ""
				if f.Exception != "" {
					errMsg, _, _ = strings.Cut(f.Exception, "\n")
				}
				out = append(out, slskTransfer{
					ID: f.ID, Username: u.Username, Name: parts[len(parts)-1], Folder: filepath.Base(d.Directory),
					Size: f.Size, State: f.State, Percent: f.PercentComplete, Speed: f.AverageSpeed, Error: errMsg,
				})
			}
		}
	}
	return out, nil
}

// SoulseekCancel cancela (y quita de la lista) una descarga.
func (a *App) SoulseekCancel(username, id string) error {
	return slsk.api(a.ctx, http.MethodDelete, "/api/v0/transfers/downloads/"+url.PathEscape(username)+"/"+url.PathEscape(id)+"?remove=true", nil, nil)
}

// SoulseekClearDone quita de la lista las descargas terminadas.
func (a *App) SoulseekClearDone() error {
	return slsk.api(a.ctx, http.MethodDelete, "/api/v0/transfers/downloads/all/completed", nil, nil)
}
