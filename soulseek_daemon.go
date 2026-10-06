package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"music_picker/internal/tools"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// El motor de Soulseek es slskd, un programa aparte: aquí se arranca y se
// para, y se le habla por su API HTTP en localhost. Lo que la ventana pide
// (buscar, descargar, transferencias) está en soulseek.go.

// slskdPort: el puerto de la API de slskd en 127.0.0.1 (no el de la red
// Soulseek, que es SlskPort en los ajustes).
const slskdPort = 5030

// slskd envuelve el proceso y la clave de API generada para esta sesión.
type slskd struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	apiKey string
	base   string // http://127.0.0.1:5030
	log    *os.File
}

var slsk = &slskd{}

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
	for _, d := range []string{appDir, incomplete, downloads, filepath.Join(tools.BinDir(), "wwwroot")} {
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
		"--app-dir", appDir,
		"--downloads", downloads, "--incomplete", incomplete,
		"--shared", share,
		"--slsk-listen-port", strconv.Itoa(port),
		"--slsk-description", "Trovador (slskd)",
	}
	// Un slskd de una sesión anterior que no se pudo parar (la app se cerró
	// de golpe) seguiría ocupando el puerto con otra clave de API: el nuevo
	// se cerraría al ver que «ya hay uno» y el viejo rechazaría todo.
	if n := killOrphans(tools.Path("slskd.exe")); n > 0 {
		a.libLog("soulseek: cerrado un slskd que había quedado de otra sesión")
	}
	cmd := exec.Command(tools.Path("slskd.exe"), args...)
	// Los secretos van por variables de entorno y no como argumentos: la
	// línea de órdenes de un proceso la puede leer cualquier usuario del
	// equipo (ps, /proc/<pid>/cmdline); su entorno, solo el dueño.
	cmd.Env = append(os.Environ(),
		"SLSKD_API_KEY="+s.apiKey,
		"SLSKD_SLSK_USERNAME="+user,
		"SLSKD_SLSK_PASSWORD="+pass,
	)
	tools.HideWindow(cmd)
	if s.log != nil {
		s.log.Close()
	}
	s.log, _ = os.Create(filepath.Join(appDir, "slskd.log"))
	cmd.Stdout, cmd.Stderr = s.log, s.log
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("no puedo arrancar slskd: %w", err)
	}
	// Que muera con la app aunque esta no llegue a pararlo.
	if err := tieToApp(cmd); err != nil {
		a.libLog("soulseek: no he podido atar slskd a la app (%v); si la app se cierra de golpe, puede quedarse en marcha", err)
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
