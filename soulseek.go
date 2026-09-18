package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Pestaña Soulseek. El protocolo lo lleva slskd (https://github.com/slskd/slskd),
// un demonio Soulseek de código abierto que la app descarga a bin\ cuando se
// usa por primera vez, arranca de fondo (sin interfaz propia, solo escuchando
// en 127.0.0.1) y maneja por su API HTTP: buscar, ver resultados, descargar y
// seguir las transferencias. Igual que con yt-dlp: el motor es de otros, la
// integración con la biblioteca es nuestra.

const slskdPort = 5030

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

// slskd envuelve el proceso y la clave de API generada para esta sesión.
type slskd struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	apiKey string
	base   string // http://127.0.0.1:5030
	log    *os.File
}

var slsk = &slskd{}

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

// ---- proceso slskd ---------------------------------------------------------

func (s *slskd) alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && s.cmd.ProcessState == nil
}

// start arranca slskd en modo sin interfaz, escuchando solo en localhost, con
// una clave de API aleatoria para esta sesión. Sus datos (base de datos de
// transferencias, caché de compartidos, log) van a %APPDATA%\Trovador\slskd.
func (s *slskd) start(a *App, user, pass, share, downloads string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.ProcessState == nil {
		return nil // ya en marcha
	}
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	appDir := filepath.Join(filepath.Dir(cfgPath), "slskd")
	incomplete := filepath.Join(appDir, "incompleto")
	// slskd valida que exista wwwroot (su interfaz web) aunque vaya headless;
	// vacía le basta.
	for _, d := range []string{appDir, incomplete, downloads, filepath.Join(binDir(), "wwwroot")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if share == "" || !fileExists(share) {
		return fmt.Errorf("la carpeta compartida no existe: %s", share)
	}

	key := make([]byte, 16)
	rand.Read(key)
	s.apiKey = hex.EncodeToString(key)
	s.base = "http://127.0.0.1:" + strconv.Itoa(slskdPort)

	args := []string{
		"--headless", "--no-logo", "--no-version-check", "--no-https",
		"--http-ip-address", "127.0.0.1", "--http-port", strconv.Itoa(slskdPort),
		"--api-key", s.apiKey,
		"--app-dir", appDir,
		"--downloads", downloads, "--incomplete", incomplete,
		"--shared", share,
		"--slsk-username", user, "--slsk-password", pass,
		"--slsk-listen-port", strconv.Itoa(port),
		"--slsk-description", "Trovador (slskd)",
	}
	cmd := exec.Command(toolPath("slskd.exe"), args...)
	hideWindow(cmd)
	if s.log != nil {
		s.log.Close()
	}
	s.log, _ = os.Create(filepath.Join(appDir, "slskd.log"))
	cmd.Stdout, cmd.Stderr = s.log, s.log
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("no puedo arrancar slskd: %w", err)
	}
	s.cmd = cmd
	go func() {
		cmd.Wait()
		a.libLog("soulseek: el motor slskd se ha parado")
		a.emit("slsk")
	}()

	// Esperar a que la API responda. slskd no la levanta hasta haber leído
	// la carpeta compartida: con una biblioteca grande, la primera vez son
	// varios minutos (luego queda en caché). Mientras, se informa del avance
	// del escaneo leyendo su log ("Scanned 25% of shared directories").
	logPath := filepath.Join(appDir, "slskd.log")
	deadline := time.Now().Add(15 * time.Minute)
	lastPct := -1
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(slskdPort), 300*time.Millisecond); err == nil {
			conn.Close()
			var v any
			if err := s.apiLocked(a.ctx, http.MethodGet, "/api/v0/application", nil, &v); err == nil {
				a.emit("slsk-progress", map[string]any{"name": "compartidos", "pct": 100, "done": true})
				a.libLog("soulseek: motor en marcha (compartiendo %s)", share)
				return nil
			}
		}
		if cmd.ProcessState != nil {
			return fmt.Errorf("slskd terminó al arrancar; mira %s", logPath)
		}
		if pct := scanProgress(logPath); pct != lastPct {
			lastPct = pct
			a.emit("slsk-progress", map[string]any{"name": "compartidos", "pct": pct})
		}
		time.Sleep(700 * time.Millisecond)
	}
	return fmt.Errorf("slskd no responde en el puerto %d tras 15 minutos; mira %s", slskdPort, logPath)
}

func (s *slskd) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.ProcessState == nil {
		// Desconexión limpia de la red y luego fin del proceso.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		s.apiLocked(ctx, http.MethodDelete, "/api/v0/server", map[string]any{}, nil)
		cancel()
		s.cmd.Process.Kill()
	}
	s.cmd = nil
}

// api hace una petición JSON a slskd con la clave de la sesión.
func (s *slskd) api(ctx context.Context, method, path string, body any, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiLocked(ctx, method, path, body, out)
}

func (s *slskd) apiLocked(ctx context.Context, method, path string, body any, out any) error {
	if s.base == "" {
		return fmt.Errorf("motor parado")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("slskd %s: %s", resp.Status, strings.Trim(strings.TrimSpace(string(msg)), `"`))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
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

var scannedRe = regexp.MustCompile(`Scanned (\d+)% of shared directories`)

// scanProgress lee el final del log de slskd y devuelve el último porcentaje
// de escaneo de compartidos que aparece (0 si aún no ha empezado).
func scanProgress(logPath string) int {
	f, err := os.Open(logPath)
	if err != nil {
		return 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	const tail = 4096
	off := info.Size() - tail
	if off < 0 {
		off = 0
	}
	buf := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return 0
	}
	pct := 0
	for _, m := range scannedRe.FindAllSubmatch(buf, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			pct = n
		}
	}
	return pct
}
