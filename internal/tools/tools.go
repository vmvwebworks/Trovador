// Package tools gestiona las herramientas externas que usa Trovador (yt-dlp,
// ffmpeg y ffprobe, deno, slskd): dónde están, qué versión tienen, y
// descargarlas si faltan. El resto del programa solo pregunta su ruta con
// Path y lanza los procesos con HideWindow puesto.
//
// Es el primer paquete aparte de main: lo que empieza por mayúscula (Path,
// Ensure, Status...) se ve desde fuera como tools.Path; lo que empieza por
// minúscula (fetch, defs...) es interno del paquete.
package tools

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Las herramientas externas viven en bin/ junto al ejecutable. Si faltan,
// la app las descarga de sus repositorios oficiales en GitHub.
//
// Este fichero es un buen ejemplo de la biblioteca estándar de Go haciendo
// "cosas de sistema" sin dependencias: HTTP, zip, ficheros y subprocesos.

type def struct {
	Name     string   // nombre para mostrar
	URL      string   // de dónde descargarlo; vacía: se usa la del sistema (PATH)
	Files    []string // ejecutables que aporta; si la URL es un zip, se extraen de él
	Optional bool     // no se descarga al arrancar, solo cuando se necesita
}

// defs (qué herramientas y de dónde) depende del sistema: está en
// defs_windows.go y defs_linux.go.

// Status es lo que ve la interfaz de cada herramienta.
type Status struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Version string `json:"version"`
}

// BinDir devuelve la carpeta bin/ al lado del .exe. La variable de entorno
// MUSIC_PICKER_BIN la sobreescribe (útil en tests y en `wails dev`, donde el
// binario se ejecuta desde una carpeta temporal).
func BinDir() string {
	if dir := os.Getenv("MUSIC_PICKER_BIN"); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		return "bin"
	}
	return filepath.Join(filepath.Dir(exe), "bin")
}

// Path es la ruta completa de una herramienta por su nombre de fichero.
// Los nombres se escriben con .exe (el de Windows); en otros sistemas se le
// quita, así el resto del código no tiene que preocuparse.
func Path(file string) string {
	if runtime.GOOS != "windows" {
		file = strings.TrimSuffix(file, ".exe")
	}
	return filepath.Join(BinDir(), file)
}

// Statuses comprueba qué hay en bin/ y pregunta la versión a cada una.
func Statuses() []Status {
	out := make([]Status, 0, len(defs))
	for _, t := range defs {
		if t.Optional && t.missing() {
			continue // no instalada y no hace falta: no se muestra
		}
		p := Path(t.Files[0])
		st := Status{Name: t.Name, Path: p, Present: !t.missing()}
		if st.Present {
			st.Version = version(t.Name, p)
		}
		out = append(out, st)
	}
	return out
}

// version ejecuta "<tool> --version" y se queda con lo interesante.
func version(name, path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	arg := "--version"
	if name == "ffmpeg" {
		arg = "-version"
	}
	cmd := exec.CommandContext(ctx, path, arg)
	HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "?"
	}
	return parseVersion(name, string(out))
}

// parseVersion saca la versión de lo que imprime "<tool> --version".
func parseVersion(name, out string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	// "ffmpeg version N-126537-g7523428c26-20260913 Copyright..." -> "N-126537-..."
	if name == "ffmpeg" {
		if f := strings.Fields(first); len(f) >= 3 {
			return f[2]
		}
	}
	// slskd imprime un logo y luego "0.26.0.0 (...)": la primera línea que
	// empieza por dígito.
	if name == "slskd" {
		for _, line := range strings.Split(out, "\n") {
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

// Progress recibe el avance de una descarga: nombre, bytes bajados y
// total (-1 si el servidor no lo dice).
type Progress func(name string, done, total int64)

// Ensure descarga las herramientas que falten. Devuelve el primer
// error; las que ya estaban no se tocan.
func Ensure(ctx context.Context, progress Progress) error {
	if err := os.MkdirAll(BinDir(), 0o755); err != nil {
		return fmt.Errorf("creando %s: %w", BinDir(), err)
	}
	for _, t := range defs {
		if t.Optional || !t.missing() {
			continue
		}
		if err := fetch(ctx, t, progress); err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
	}
	return nil
}

// Install descarga una herramienta opcional (las que no se bajan al arrancar,
// como slskd) si falta. Devuelve si la ha descargado.
func Install(ctx context.Context, name string, progress Progress) (bool, error) {
	for _, t := range defs {
		if t.Name != name {
			continue
		}
		if !t.missing() {
			return false, nil
		}
		return true, fetch(ctx, t, progress)
	}
	return false, fmt.Errorf("herramienta desconocida: %s", name)
}

// missing dice si falta alguno de los ejecutables de la herramienta.
func (t def) missing() bool {
	for _, f := range t.Files {
		if _, err := os.Stat(Path(f)); err != nil {
			return true
		}
	}
	return false
}

// fetch descarga una herramienta a un fichero temporal y, si viene en
// zip, extrae sus ejecutables. Solo al final renombra al destino: así nunca
// queda un binario a medias con el nombre bueno.
func fetch(ctx context.Context, t def, progress Progress) error {
	if t.URL == "" {
		return linkSystem(t)
	}
	tmp, err := os.CreateTemp(BinDir(), t.Name+".*.part")
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
		// CreateTemp lo crea con 0600: sin permiso de ejecución en Linux.
		if err := os.Chmod(tmpName, 0o755); err != nil {
			return err
		}
		return os.Rename(tmpName, Path(t.Files[0]))
	}
	for _, f := range t.Files {
		dest := Path(f)
		if err := extractFromZip(tmpName, filepath.Base(dest), dest); err != nil {
			return err
		}
	}
	return nil
}

// linkSystem enlaza en bin/ los ejecutables que ya están instalados en
// el sistema (ffmpeg en Linux, que solo se publica en .tar.xz).
func linkSystem(t def) error {
	for _, f := range t.Files {
		dest := Path(f)
		src, err := exec.LookPath(filepath.Base(dest))
		if err != nil {
			return fmt.Errorf("no está instalado %s (sudo apt install %s)", filepath.Base(dest), t.Name)
		}
		os.Remove(dest) // por si queda un enlace roto
		if err := os.Symlink(src, dest); err != nil {
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

// UpdateYtdlp usa el actualizador propio de yt-dlp ("-U").
func UpdateYtdlp(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, Path("yt-dlp.exe"), "-U")
	HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return msg, fmt.Errorf("yt-dlp -U: %w", err)
	}
	return msg, nil
}
