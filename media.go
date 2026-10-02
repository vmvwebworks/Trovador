package main

import (
	"context"
	"encoding/json"
	"fmt"
	"music_picker/internal/tools"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ffprobe y ffmpeg: leer las etiquetas y la duración de un fichero, sacar
// la carátula incrustada y reescribir un fichero con etiquetas y carátula
// nuevas (sin recodificar el audio). Lo usan la Biblioteca, crear álbum,
// las duraciones (lengths.go) y completar desde Soulseek.

// probe lee un fichero de audio con ffprobe: duración, etiquetas y datos
// técnicos del flujo de audio, y si lleva una imagen incrustada.
func probe(ctx context.Context, path string) (localTrack, error) {
	t := localTrack{Path: path, Name: filepath.Base(path)}
	cmd := exec.CommandContext(ctx, tools.Path("ffprobe.exe"), "-v", "error",
		"-show_entries", "format=duration,bit_rate,size:format_tags"+
			":stream=codec_type,codec_name,sample_rate,channels,bit_rate:stream_disposition=attached_pic",
		"-of", "json", path)
	tools.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return t, err
	}
	// Los números vienen como cadenas en el JSON de ffprobe.
	var raw struct {
		Format struct {
			Duration string            `json:"duration"`
			BitRate  string            `json:"bit_rate"`
			Size     string            `json:"size"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Type        string `json:"codec_type"`
			Codec       string `json:"codec_name"`
			SampleRate  string `json:"sample_rate"`
			Channels    int    `json:"channels"`
			BitRate     string `json:"bit_rate"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return t, err
	}
	t.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	t.Size, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	bitrate, _ := strconv.Atoi(raw.Format.BitRate)

	// ffprobe puede devolver las claves en mayúsculas (ID3) o minúsculas.
	tags := make(map[string]string, len(raw.Format.Tags))
	for k, v := range raw.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	t.Title, t.Artist, t.Album = tags["title"], tags["artist"], tags["album"]
	t.AlbumArtist, t.Track, t.Date = tags["album_artist"], tags["track"], tags["date"]
	t.Media = tags["media"]
	t.ArtistMBID = firstNonEmpty(tags["musicbrainz_albumartistid"], tags["musicbrainz album artist id"], tags["musicbrainz_artistid"], tags["musicbrainz artist id"])

	for _, s := range raw.Streams {
		switch {
		case s.Type == "audio" && t.Codec == "":
			t.Codec = s.Codec
			t.SampleRate, _ = strconv.Atoi(s.SampleRate)
			t.Channels = s.Channels
			if br, _ := strconv.Atoi(s.BitRate); br > 0 {
				bitrate = br // el del flujo es más preciso que el del contenedor
			}
		case s.Type == "video" && (s.Disposition.AttachedPic == 1 || s.Codec == "mjpeg" || s.Codec == "png"):
			t.HasCover = true
		}
	}
	t.Bitrate = bitrate / 1000
	return t, nil
}

// embeddedCover extrae la imagen incrustada de un fichero de audio (bytes
// JPEG o PNG tal cual, sin recodificar). Error si no tiene.
func embeddedCover(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, tools.Path("ffmpeg.exe"), "-v", "error",
		"-i", path, "-an", "-map", "0:v:0", "-c:v", "copy", "-f", "image2pipe", "-")
	tools.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil || len(out) < 100 {
		return nil, fmt.Errorf("sin carátula incrustada")
	}
	return out, nil
}

// rewrite reescribe un fichero de audio sin recodificar (-c copy) cambiando
// etiquetas y/o carátula. in y out pueden ser el mismo fichero: se escribe a
// un temporal y se sustituye al final.
func rewrite(ctx context.Context, in, out string, tags map[string]string, cover string) error {
	return rewriteArgs(ctx, in, out, nil, tags, cover)
}

// rewriteArgs es rewrite con opciones extra de entrada (p. ej. -ss/-t para
// cortar un trozo). Ojo al convenio: con inputArgs == nil el fichero de
// entrada se elimina al terminar (es un renombrado/movimiento); con un slice
// no nil, aunque esté vacío, se conserva (es una copia o un corte).
func rewriteArgs(ctx context.Context, in, out string, inputArgs []string, tags map[string]string, cover string) error {
	tmp := filepath.Join(filepath.Dir(out), ".tmp-"+filepath.Base(out))

	args := []string{"-v", "error", "-y"}
	args = append(args, inputArgs...)
	args = append(args, "-i", in)
	if cover != "" && coverSupported(out) {
		// Segunda entrada: la imagen. Nos quedamos solo con el audio de la
		// primera (descarta una carátula vieja) y la imagen de la segunda.
		args = append(args, "-i", cover, "-map", "0:a", "-map", "1", "-disposition:v", "attached_pic")
	} else {
		args = append(args, "-map", "0")
	}
	args = append(args, "-c", "copy", "-id3v2_version", "3")
	for k, v := range tags {
		args = append(args, "-metadata", k+"="+v)
	}
	args = append(args, tmp)

	cmd := exec.CommandContext(ctx, tools.Path("ffmpeg.exe"), args...)
	tools.HideWindow(cmd)
	if outb, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(string(outb)))
	}
	// Sustituir: si out ya existe (incluido el caso in == out) lo quitamos
	// y ponemos el temporal en su lugar.
	if fileExists(out) {
		if err := os.Remove(out); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("no puedo reemplazar %s (¿está abierto en un reproductor?): %w", filepath.Base(out), err)
		}
	}
	if err := os.Rename(tmp, out); err != nil {
		return err
	}
	// Renombrado (etiquetar con otro nombre): el fichero viejo sobra. Al
	// cortar trozos (inputArgs) el original se conserva; lo aparta quien llama.
	if inputArgs == nil && !samePath(in, out) {
		os.Remove(in)
	}
	return nil
}

// coverSupported: formatos donde ffmpeg sabe incrustar una imagen con -c copy.
func coverSupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".m4a", ".flac":
		return true
	}
	return false
}

// trackTags son las etiquetas estándar para una pista de una edición.
func trackTags(rel mbReleaseDetail, t mbTrack) map[string]string {
	artist := t.Artist
	if artist == "" {
		artist = rel.Release.Artist
	}
	tags := map[string]string{
		"title":        t.Title,
		"artist":       artist,
		"album":        rel.Release.Title,
		"album_artist": rel.Release.Artist,
		"track":        fmt.Sprintf("%d/%d", t.Number, len(rel.Tracks)),
	}
	if y := yearOf(rel.Release.Date); y != "" {
		tags["date"] = y
	}
	if f := strings.TrimSpace(rel.Release.Format); f != "" {
		tags["media"] = f // la misma etiqueta que escribe Picard; sirve para el icono de carpeta
	}
	if rel.Release.Source == srcMusicBrainz {
		tags["musicbrainz_albumid"] = rel.Release.ID
		if rel.Release.FirstArtistID != "" {
			tags["musicbrainz_albumartistid"] = rel.Release.FirstArtistID // identifica al artista sin ambigüedad (foto, carpeta)
		}
	}
	return tags
}

// ffTime formatea segundos como los quiere ffmpeg: "123.456".
func ffTime(sec float64) string { return strconv.FormatFloat(sec, 'f', 3, 64) }
