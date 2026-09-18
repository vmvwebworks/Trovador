package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// config son los parámetros de una tanda de descargas. Se construye a partir
// de los ajustes guardados (settings) más lo que el usuario elija en la ventana.
type config struct {
	outDir  string // carpeta de destino
	workers int    // descargas simultáneas
	format  string // mp3, m4a, opus, flac...
	quality string // "0" (mejor) o un bitrate como "192K"
}

// result es lo que devuelve cada descarga.
type result struct {
	url   string
	paths []string // ficheros finales (vacío si falló)
	err   error    // nil significa que fue bien
}

// logFunc recibe cada línea de salida. Pasar una función en vez de escribir
// directamente a la consola desacopla la descarga de quien la muestra: hoy
// es la ventana, pero podría ser un fichero de log o una consola.
type logFunc func(line string)

// downloadAll reparte los enlaces entre `cfg.workers` goroutines.
//
// Patrón "worker pool":
//   - un canal `jobs` por el que se envían los trabajos,
//   - N goroutines que leen de ese canal hasta que se cierra,
//   - un WaitGroup para esperar a que todas terminen.
//
// Los resultados se escriben en un slice compartido; es seguro porque cada
// goroutine escribe en una posición distinta (results[i]) y nadie lo lee
// hasta después del wg.Wait().
func downloadAll(ctx context.Context, urls []string, cfg config, log logFunc) []result {
	type job struct {
		index int
		url   string
	}

	jobs := make(chan job)
	results := make([]result, len(urls))
	var wg sync.WaitGroup

	for w := 0; w < cfg.workers; w++ {
		wg.Add(1)
		// `go` lanza la función en una goroutine (hilo ligero). La closure
		// captura jobs, results y cfg del ámbito exterior.
		go func() {
			defer wg.Done()
			for j := range jobs { // termina cuando se cierra el canal
				paths, err := download(ctx, j.index+1, j.url, cfg, log)
				results[j.index] = result{url: j.url, paths: paths, err: err}
			}
		}()
	}

	// El productor: envía los trabajos. `select` espera a lo primero que
	// ocurra: que un worker acepte el trabajo, o que el usuario cancele
	// (Ctrl+C); en ese caso marcamos el resto como cancelados sin encolar.
	for i, u := range urls {
		select {
		case jobs <- job{index: i, url: u}:
		case <-ctx.Done():
			results[i] = result{url: u, err: ctx.Err()}
		}
	}
	close(jobs)
	wg.Wait()

	return results
}

// download ejecuta yt-dlp para un único enlace y va imprimiendo su salida
// con el prefijo [n] para distinguir descargas paralelas. Devuelve las rutas
// de los ficheros finales (uno por vídeo; si se troceó, las pistas).
func download(ctx context.Context, n int, url string, cfg config, log logFunc) ([]string, error) {
	args := buildArgs(url, cfg)

	// exec.CommandContext: si ctx se cancela, el proceso hijo se mata.
	cmd := exec.CommandContext(ctx, toolPath("yt-dlp.exe"), args...)
	hideWindow(cmd)
	// Con bin/ en el PATH, yt-dlp encuentra ffmpeg y deno solo. PYTHONUTF8
	// obliga a yt-dlp (que es Python) a escribir UTF-8 aunque la consola de
	// Windows tenga otra codificación; si no, los títulos con acentos que
	// leemos de su salida llegarían rotos.
	cmd.Env = append(os.Environ(),
		"PATH="+binDir()+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
	)

	// Leemos stdout y stderr por la misma tubería para no perder los
	// errores de yt-dlp, que van por stderr.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("no puedo lanzar yt-dlp: %w", err)
	}

	var finals []string
	var lastLines []string
	var chapterFiles []string // pistas generadas por el troceado del vídeo actual
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()

		// Las líneas con nuestro marcador las imprime `--print after_move`:
		// una por vídeo, al final, con la ruta del fichero ya terminado.
		if final, ok := strings.CutPrefix(line, finalMarker); ok {
			if len(chapterFiles) > 0 {
				// Se troceó por capítulos: el fichero largo sobra.
				if err := os.Remove(final); err != nil {
					log(fmt.Sprintf("[%d] aviso: no pude borrar %s: %v", n, final, err))
				} else {
					log(fmt.Sprintf("[%d] %d pistas separadas; borrado el fichero completo", n, len(chapterFiles)))
				}
				finishChapters(ctx, chapterFiles, func(s string) { log(fmt.Sprintf("[%d] %s", n, s)) })
				finals = append(finals, chapterFiles...)
				chapterFiles = nil
			} else {
				finals = append(finals, final)
			}
			continue
		}
		if p, ok := strings.CutPrefix(line, "[SplitChapters] Chapter "); ok {
			// Formato: "Chapter 001; Destination: /ruta/01 - Título.mp3"
			if _, dest, ok := strings.Cut(p, "; Destination: "); ok {
				chapterFiles = append(chapterFiles, dest)
			}
		}

		log(fmt.Sprintf("[%d] %s", n, line))
		// Guardamos las últimas líneas para dar contexto si falla.
		lastLines = append(lastLines, line)
		if len(lastLines) > 3 {
			lastLines = lastLines[1:]
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return finals, ctx.Err() // cancelado por el usuario, no es un fallo real
		}
		return finals, fmt.Errorf("yt-dlp terminó con error: %w\n         %s",
			err, strings.Join(lastLines, "\n         "))
	}
	return finals, nil
}

// buildArgs construye la línea de comandos de yt-dlp. Aquí está la lógica
// de "producto": qué formato, dónde guardar y cómo nombrar los ficheros.
func buildArgs(url string, cfg config) []string {
	// Canciones sueltas van a la raíz; las playlists a una subcarpeta con
	// su nombre y numeradas para conservar el orden.
	template := "%(title)s.%(ext)s"
	if isPlaylist(url) {
		// %(track,title)s: el nombre de pista si la fuente lo da (Bandcamp), si
		// no el título. Evita "Artista - Artista - Pista" en recopilaciones.
		template = "%(playlist_title)s/%(playlist_index)02d - %(track,title)s.%(ext)s"
	}

	// Si el vídeo tiene capítulos (un disco entero con marcadores de tiempo),
	// yt-dlp lo trocea en una pista por capítulo dentro de una carpeta con
	// el nombre del vídeo: "Título/01 - Pista.mp3".
	chapterTemplate := strings.TrimSuffix(template, ".%(ext)s") + "/%(section_number)02d - %(section_title)s.%(ext)s"

	args := []string{
		"--no-warnings",
		"--encoding", "utf-8",
		"--ffmpeg-location", binDir(),
		"--newline",       // una línea por actualización de progreso (mejor para logs)
		"--no-overwrites", // no volver a bajar lo que ya existe
		"--ignore-errors", // en una playlist, un vídeo roto no para el resto
		// Registro de IDs ya descargados: al repetir una lista se saltan
		// aunque el fichero se haya movido o troceado.
		"--download-archive", filepath.Join(cfg.outDir, ".descargado.txt"),
		"-f", "bestaudio/best",
		"-x", // extraer solo el audio
		"--audio-format", cfg.format,
		"--audio-quality", cfg.quality,
		"--embed-thumbnail", // carátula
		"--embed-metadata",  // título, artista, etc. en las etiquetas ID3
		"--split-chapters",
		"--progress-template", "download:  %(progress._percent_str)s  %(progress._speed_str)s  ETA %(progress._eta_str)s",
		"-P", cfg.outDir,
		"-o", template,
		"-o", "chapter:" + chapterTemplate,
		// Al acabar cada vídeo, que nos diga la ruta del fichero final.
		// --print implica modo silencioso; --no-quiet lo deshace.
		"--print", "after_move:" + finalMarker + "%(filepath)s",
		"--no-quiet",
	}

	// Un "Mix" de YouTube (list=RD...) es una radio infinita generada al vuelo:
	// yt-dlp puede resolverla a cientos de vídeos. Nos quedamos solo con el
	// vídeo del enlace, que es lo que el usuario estaba escuchando.
	if isMix(url) {
		args = append(args, "--no-playlist")
	}

	// "--" indica que lo que sigue es la URL aunque empiece por guion.
	return append(args, "--", url)
}

// finalMarker es el prefijo que le pedimos a yt-dlp que imprima delante de
// la ruta final de cada vídeo, para distinguir esa línea de todo lo demás.
const finalMarker = "@@final@@ "

// leadingNumber reconoce "1. ", "01 - ", "3) ", "12: " al principio del
// título de un capítulo. Los autores suelen numerar los marcadores y, como
// ya ponemos nuestro número, quedaría "01 - 1. Canción".
//
// regexp.MustCompile a nivel de paquete: se compila una sola vez al arrancar.
var leadingNumber = regexp.MustCompile(`^(\d{2} - )\d{1,3}\s*[.):\-–]?\s+`)

// finishChapters deja las pistas troceadas como si fueran un disco normal:
// limpia la numeración duplicada del nombre y escribe las etiquetas (título,
// número de pista, álbum), porque yt-dlp las deja con las del vídeo entero.
// ffmpeg con "-c copy" solo reescribe la cabecera: no recodifica el audio.
func finishChapters(ctx context.Context, files []string, log logFunc) {
	for _, f := range files {
		dir, base := filepath.Split(f)
		clean := leadingNumber.ReplaceAllString(base, "$1")

		// "02 - Título.mp3" -> número "02", título "Título"
		ext := filepath.Ext(clean)
		num, title, _ := strings.Cut(strings.TrimSuffix(clean, ext), " - ")
		album := filepath.Base(filepath.Clean(dir))

		tmp := dir + ".tmp-" + clean
		cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-i", f, "-map", "0", "-c", "copy", "-id3v2_version", "3",
			"-metadata", "title="+title,
			"-metadata", "track="+num,
			"-metadata", "album="+album,
			tmp)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			log(fmt.Sprintf("aviso: no pude etiquetar %s: %v %s", base, err, strings.TrimSpace(string(out))))
			os.Remove(tmp)
			continue
		}
		// Sustituimos el original por el etiquetado, ya con el nombre limpio.
		if err := os.Remove(f); err == nil {
			if err := os.Rename(tmp, dir+clean); err != nil {
				log(fmt.Sprintf("aviso: no pude renombrar %s: %v", base, err))
			}
		}
	}
}

// isMix detecta las listas automáticas de YouTube (Mix / radio).
func isMix(url string) bool {
	return strings.Contains(url, "list=RD") || strings.Contains(url, "start_radio=")
}

// isPlaylist decide, por la forma del enlace, si yt-dlp va a descargar una
// lista. Es una heurística sencilla pero cubre los enlaces habituales:
// YouTube (list=, /playlist) y Bandcamp (/album/).
func isPlaylist(url string) bool {
	if isMix(url) {
		return false
	}
	for _, marker := range []string{"list=", "/playlist", "/album/"} {
		if strings.Contains(url, marker) {
			return true
		}
	}
	return false
}
