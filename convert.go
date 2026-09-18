package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Convertir pistas de formato desde el reproductor: hay ficheros que la
// ventana (Chromium) no sabe reproducir —WMA, APE, WavPack, AIFF, ALAC
// dentro de un .m4a...— y otros que simplemente se quieren en MP3 o FLAC.
// Se transcodifica con ffmpeg conservando etiquetas y carátula; el fichero
// nuevo queda junto al original con la extensión nueva, y el original se
// aparta a _original\ (como al separar pistas).

// convFormat describe un formato de destino de la lista de la ventana.
type convFormat struct {
	Ext   string   // extensión del fichero nuevo
	Args  []string // codificador y calidad
	Cover bool     // admite carátula incrustada como flujo attached_pic
}

var convFormats = map[string]convFormat{
	"mp3-320": {".mp3", []string{"-c:a", "libmp3lame", "-b:a", "320k", "-id3v2_version", "3"}, true},
	"mp3-v0":  {".mp3", []string{"-c:a", "libmp3lame", "-q:a", "0", "-id3v2_version", "3"}, true},
	"flac":    {".flac", []string{"-c:a", "flac"}, true},
	"m4a-256": {".m4a", []string{"-c:a", "aac", "-b:a", "256k", "-movflags", "+faststart"}, true},
}

// ConvertFormats: los formatos que ofrece la ventana, en orden.
func (a *App) ConvertFormats() []string { return []string{"mp3-320", "mp3-v0", "flac", "m4a-256"} }

// ConvertTrack convierte un fichero al formato dado y devuelve la ruta del
// nuevo. Si ya está en ese formato, lo dice y no hace nada.
func (a *App) ConvertTrack(path, format string) (string, error) {
	f, ok := convFormats[format]
	if !ok {
		return "", fmt.Errorf("formato desconocido: %s", format)
	}
	if strings.EqualFold(filepath.Ext(path), f.Ext) {
		return "", fmt.Errorf("%s ya es %s", filepath.Base(path), strings.TrimPrefix(f.Ext, "."))
	}
	if !fileExists(path) {
		return "", fmt.Errorf("no existe %s", path)
	}
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := filepath.Join(dir, base+f.Ext)
	if fileExists(out) {
		out = filepath.Join(dir, base+" (2)"+f.Ext)
	}
	a.libLog("convirtiendo %s -> %s", filepath.Base(path), strings.TrimPrefix(f.Ext, "."))
	if err := transcode(a.ctx, path, out, f); err != nil {
		return "", err
	}
	// Apartar el original, no borrarlo.
	orig := filepath.Join(dir, "_original")
	if err := os.MkdirAll(orig, 0o755); err == nil {
		if err := os.Rename(path, filepath.Join(orig, filepath.Base(path))); err != nil {
			a.libLog("aviso: no pude apartar el original: %v", err)
		}
	}
	a.libLog("listo: %s (original en _original\\)", filepath.Base(out))
	return out, nil
}

// ConvertAlbum convierte todas las pistas de la carpeta que no estén ya en
// ese formato. Devuelve las rutas nuevas.
func (a *App) ConvertAlbum(dir, format string) ([]string, error) {
	f, ok := convFormats[format]
	if !ok {
		return nil, fmt.Errorf("formato desconocido: %s", format)
	}
	var outs []string
	for _, t := range audioTracks(dir) {
		if a.ctx.Err() != nil {
			return outs, a.ctx.Err()
		}
		if strings.EqualFold(filepath.Ext(t.Name), f.Ext) {
			continue
		}
		out, err := a.ConvertTrack(filepath.Join(dir, t.Name), format)
		if err != nil {
			a.libLog("aviso: %s: %v", t.Name, err)
			continue
		}
		outs = append(outs, out)
	}
	a.libLog("Convertidas %d pistas en %s", len(outs), filepath.Base(dir))
	return outs, nil
}

// transcode recodifica in a out: solo el primer flujo de audio, todas las
// etiquetas y, si el formato lo admite, la carátula incrustada tal cual
// (sin recomprimirla). Si ffmpeg tropieza con la imagen, se repite sin ella.
func transcode(ctx context.Context, in, out string, f convFormat) error {
	tmp := filepath.Join(filepath.Dir(out), ".tmp-"+filepath.Base(out))
	run := func(withCover bool) error {
		args := []string{"-v", "error", "-y", "-i", in, "-map", "0:a:0", "-map_metadata", "0"}
		if withCover {
			args = append(args, "-map", "0:v?", "-c:v", "copy", "-disposition:v", "attached_pic")
		} else {
			args = append(args, "-vn")
		}
		args = append(args, f.Args...)
		args = append(args, tmp)
		cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), args...)
		hideWindow(cmd)
		if outb, err := cmd.CombinedOutput(); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(string(outb)))
		}
		return nil
	}
	err := run(f.Cover)
	if err != nil && f.Cover {
		err = run(false)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
