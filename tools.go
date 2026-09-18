package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Las herramientas externas viven en bin/ junto al ejecutable. Si faltan,
// la app las descarga de sus repositorios oficiales en GitHub.
//
// Este fichero es un buen ejemplo de la biblioteca estándar de Go haciendo
// "cosas de sistema" sin dependencias: HTTP, zip, ficheros y subprocesos.

type toolDef struct {
	Name     string   // nombre para mostrar
	URL      string   // de dónde descargarlo
	Files    []string // ejecutables que aporta; si la URL es un zip, se extraen de él
	Optional bool     // no se descarga al arrancar, solo cuando se necesita
}

var toolDefs = []toolDef{
	{"yt-dlp", "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe", []string{"yt-dlp.exe"}, false},
	{"ffmpeg", "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip", []string{"ffmpeg.exe", "ffprobe.exe"}, false},
	{"deno", "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip", []string{"deno.exe"}, false},
	// slskd: motor Soulseek, 60 MB; solo si se usa la pestaña Soulseek.
	{"slskd", "https://github.com/slskd/slskd/releases/download/0.26.0/slskd-0.26.0-win-x64.zip", []string{"slskd.exe"}, true},
}

// toolStatus es lo que ve la interfaz de cada herramienta.
type toolStatus struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Version string `json:"version"`
}

// binDir devuelve la carpeta bin/ al lado del .exe. La variable de entorno
// MUSIC_PICKER_BIN la sobreescribe (útil en tests y en `wails dev`, donde el
// binario se ejecuta desde una carpeta temporal).
func binDir() string {
	if dir := os.Getenv("MUSIC_PICKER_BIN"); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		return "bin"
	}
	return filepath.Join(filepath.Dir(exe), "bin")
}

// toolPath es la ruta completa de una herramienta por su nombre de fichero.
func toolPath(file string) string {
	return filepath.Join(binDir(), file)
}

// toolStatuses comprueba qué hay en bin/ y pregunta la versión a cada una.
func toolStatuses() []toolStatus {
	out := make([]toolStatus, 0, len(toolDefs))
	for _, t := range toolDefs {
		if t.Optional && t.missing() {
			continue // no instalada y no hace falta: no se muestra
		}
		p := toolPath(t.Files[0])
		st := toolStatus{Name: t.Name, Path: p, Present: !t.missing()}
		if st.Present {
			st.Version = toolVersion(t.Name, p)
		}
		out = append(out, st)
	}
	return out
}

// toolVersion ejecuta "<tool> --version" y se queda con lo interesante.
func toolVersion(name, path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	arg := "--version"
	if name == "ffmpeg" {
		arg = "-version"
	}
	cmd := exec.CommandContext(ctx, path, arg)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "?"
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	// "ffmpeg version N-126537-g7523428c26-20260913 Copyright..." -> "N-126537-..."
	if name == "ffmpeg" {
		if f := strings.Fields(first); len(f) >= 3 {
			return f[2]
		}
	}
	// slskd imprime un logo y luego "0.26.0.0 (...)": la primera línea que
	// empieza por dígito.
	if name == "slskd" {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0][0] >= '0' && f[0][0] <= '9' {
				return f[0]
			}
		}
		return "?"
	}
	// "deno 2.x.y (stable, ...)" -> "2.x.y"
	if name == "deno" {
		if f := strings.Fields(first); len(f) >= 2 {
			return f[1]
		}
	}
	return first
}

// progressFunc recibe el avance de una descarga: nombre, bytes bajados y
// total (-1 si el servidor no lo dice).
type progressFunc func(name string, done, total int64)

// ensureTools descarga las herramientas que falten. Devuelve el primer
// error; las que ya estaban no se tocan.
func ensureTools(ctx context.Context, progress progressFunc) error {
	if err := os.MkdirAll(binDir(), 0o755); err != nil {
		return fmt.Errorf("creando %s: %w", binDir(), err)
	}
	for _, t := range toolDefs {
		if t.Optional || !t.missing() {
			continue
		}
		if err := fetchTool(ctx, t, progress); err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
	}
	return nil
}

// missing dice si falta alguno de los ejecutables de la herramienta.
func (t toolDef) missing() bool {
	for _, f := range t.Files {
		if _, err := os.Stat(toolPath(f)); err != nil {
			return true
		}
	}
	return false
}

// fetchTool descarga una herramienta a un fichero temporal y, si viene en
// zip, extrae sus ejecutables. Solo al final renombra al destino: así nunca
// queda un binario a medias con el nombre bueno.
func fetchTool(ctx context.Context, t toolDef, progress progressFunc) error {
	tmp, err := os.CreateTemp(binDir(), t.Name+".*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // si todo va bien ya no existirá; si no, limpia

	if err := downloadFile(ctx, t.URL, tmp, func(done, total int64) { progress(t.Name, done, total) }); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	if !strings.HasSuffix(t.URL, ".zip") {
		return os.Rename(tmpName, toolPath(t.Files[0]))
	}
	for _, f := range t.Files {
		if err := extractFromZip(tmpName, f, toolPath(f)); err != nil {
			return err
		}
	}
	return nil
}

// downloadFile hace GET y va copiando al fichero, informando del progreso.
// io.Copy con un Reader "envuelto" es el patrón Go para observar un flujo
// sin cargarlo entero en memoria (el zip de ffmpeg pesa ~170 MB).
func downloadFile(ctx context.Context, url string, w io.Writer, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	pr := &progressReader{r: resp.Body, total: resp.ContentLength, report: progress}
	_, err = io.Copy(w, pr)
	return err
}

// progressReader implementa io.Reader: cada Read cuenta los bytes y avisa
// como mucho cada 200 ms para no inundar la interfaz de eventos.
type progressReader struct {
	r      io.Reader
	total  int64
	done   int64
	last   time.Time
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Since(p.last) > 200*time.Millisecond || err == io.EOF {
		p.last = time.Now()
		p.report(p.done, p.total)
	}
	return n, err
}

// extractFromZip busca dentro del zip un fichero cuyo nombre base sea `name`
// (viene en subcarpetas tipo ffmpeg-xxx/bin/ffmpeg.exe) y lo escribe en dest.
func extractFromZip(zipPath, name, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		out, err := os.OpenFile(dest+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return err
		}
		out.Close()
		return os.Rename(dest+".part", dest)
	}
	return fmt.Errorf("no encuentro %s dentro del zip", name)
}

// updateYtdlp usa el actualizador propio de yt-dlp ("-U").
func updateYtdlp(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, toolPath("yt-dlp.exe"), "-U")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return msg, fmt.Errorf("yt-dlp -U: %w", err)
	}
	return msg, nil
}
