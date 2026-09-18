package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Pestaña Biblioteca: comprobar un disco descargado contra MusicBrainz,
// ponerle carátula, separar un fichero largo por las duraciones oficiales
// y etiquetar/renombrar las pistas.

var audioExts = map[string]bool{".mp3": true, ".m4a": true, ".opus": true, ".flac": true, ".wav": true, ".ogg": true,
	".wma": true, ".ape": true, ".wv": true, ".aiff": true, ".aif": true, ".mpc": true} // los últimos no suenan en la ventana: se convierten

// localTrack es un fichero de audio de la carpeta, con lo que dice ffprobe.
type localTrack struct {
	Path        string  `json:"path"`
	Name        string  `json:"name"`
	Duration    float64 `json:"duration"` // segundos
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	AlbumArtist string  `json:"albumArtist"`
	Track       string  `json:"track"` // "3" o "3/11"
	Date        string  `json:"date"`
	Media       string  `json:"media"`      // formato de origen (CD, Vinyl, Cassette...), si está etiquetado
	ArtistMBID  string  `json:"artistMbid"` // MusicBrainz Album Artist Id, si está etiquetado (Picard o nosotros)
	Codec       string  `json:"codec"`      // mp3, aac, opus, flac...
	Bitrate     int     `json:"bitrate"`    // kbps
	SampleRate  int     `json:"sampleRate"` // Hz
	Channels    int     `json:"channels"`
	Size        int64   `json:"size"` // bytes
	HasCover    bool    `json:"hasCover"`
}

// folderScan es el resultado de mirar una carpeta.
type folderScan struct {
	Dir    string       `json:"dir"`
	Files  []localTrack `json:"files"`
	Cover  string       `json:"cover"`  // ruta a cover.jpg si existe
	Artist string       `json:"artist"` // sugerencias para la búsqueda
	Album  string       `json:"album"`
	Total  float64      `json:"total"` // duración total en segundos
}

// libLog manda una línea a la caja de log de la pestaña Biblioteca.
func (a *App) libLog(format string, args ...any) {
	a.emit("lib", fmt.Sprintf(format, args...))
}

// ---- Métodos expuestos a la ventana --------------------------------------

// ChooseLibraryFolder abre el diálogo para elegir cualquier carpeta.
func (a *App) ChooseLibraryFolder() (string, error) {
	a.mu.Lock()
	root := a.settings.OutDir
	a.mu.Unlock()
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Carpeta del disco", DefaultDirectory: existingDir(root),
	})
}

// ScanFolder lee los ficheros de audio de la carpeta con sus duraciones y
// etiquetas, y sugiere artista/álbum para la búsqueda.
func (a *App) ScanFolder(dir string) (folderScan, error) {
	scan := folderScan{Dir: dir, Files: []localTrack{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return scan, err
	}
	for _, e := range entries {
		if e.IsDir() || !audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		t, err := probe(a.ctx, filepath.Join(dir, e.Name()))
		if err != nil {
			a.libLog("aviso: no pude leer %s: %v", e.Name(), err)
		}
		scan.Total += t.Duration
		scan.Files = append(scan.Files, t)
	}
	sort.Slice(scan.Files, func(i, j int) bool { return naturalLess(scan.Files[i].Name, scan.Files[j].Name) })

	if _, err := os.Stat(filepath.Join(dir, "cover.jpg")); err == nil {
		scan.Cover = filepath.Join(dir, "cover.jpg")
	}

	scan.Artist, scan.Album = guessNames(filepath.Base(dir), scan.Files)
	return scan, nil
}

var (
	yearRe       = regexp.MustCompile(`^(19|20)\d\d$`)
	leadYearBare = regexp.MustCompile(`^\s*(19|20)\d\d\s+`)
)

// guessNames deduce (artista, álbum) del nombre de la carpeta cotejándolo
// con las etiquetas de los ficheros, porque el nombre engaña a menudo:
//   - "1998 - Under the Hungarian Blackmoon (Demo)": lo de la izquierda es
//     un año, no un artista; el artista sale de las etiquetas (Frost).
//   - "Under the Hungarian Blackmoon - Frost": invertido; se detecta porque
//     la derecha se parece al artista etiquetado y la izquierda al álbum.
//   - "ZVERI": sin separador; álbum = nombre, artista = etiquetas.
func guessNames(folder string, files []localTrack) (artist, album string) {
	tagArtist := commonArtist(files)
	tagAlbum := commonTag(files, func(f localTrack) string { return f.Album })

	name := cleanTitle(folder) // sin "[SWE]", "(Full Album)", "(Demo)", "(2021)"...
	if m := leadYear.FindStringSubmatch(name); m != nil {
		return tagArtist, cleanTitle(name[len(m[0]):])
	}
	// "1999 La Chanson de Guillaume": año suelto delante de un título largo
	// (un álbum llamado solo "1984" se queda como está).
	if m := leadYearBare.FindStringSubmatch(name); m != nil && len(normalizeName(name[len(m[0]):])) >= 6 {
		name = cleanTitle(name[len(m[0]):])
	}
	left, right, ok := strings.Cut(name, " - ")
	if !ok {
		return tagArtist, name
	}
	left, right = cleanTitle(left), cleanTitle(right)
	switch {
	case yearRe.MatchString(left):
		return tagArtist, right
	case tagArtist != "" && similarity(right, tagArtist) > 0.8 && similarity(left, tagArtist) < 0.5:
		return right, left // "Álbum - Artista"
	case tagAlbum != "" && similarity(left, tagAlbum) > 0.8 && similarity(right, tagAlbum) < 0.5:
		return right, left
	}
	// "Oliphant - Oliphant - Songs from the Crusades": el artista repetido no
	// forma parte del título.
	return left, stripArtistPrefix(right, left, tagArtist)
}

// commonTag devuelve el valor de una etiqueta cuando (casi) todos los
// ficheros coinciden; "" si no hay o si es una mezcla.
func commonTag(files []localTrack, get func(localTrack) string) string {
	counts := map[string]int{}
	for _, f := range files {
		if v := strings.TrimSpace(get(f)); v != "" {
			counts[v]++
		}
	}
	best, n := "", 0
	for v, c := range counts {
		if c > n {
			best, n = v, c
		}
	}
	if len(files) > 0 && n*2 < len(files) { // menos de la mitad: no es común
		return ""
	}
	return best
}

// CoverData devuelve una imagen como data URL para mostrarla en la ventana
// (WebView2 no permite cargar file:// desde la página).
func (a *App) CoverData(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// searchOpts: opciones de búsqueda según los ajustes actuales.
func (a *App) searchOpts(fuzzy bool) searchOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return searchOptions{Fuzzy: fuzzy, DiscogsToken: strings.TrimSpace(a.settings.DiscogsToken)}
}

// searchResult es lo que devuelve una búsqueda a la ventana: candidatas de
// todas las fuentes y los avisos de las que fallaron.
type searchResult struct {
	Results  []mbRelease `json:"results"`
	Warnings []string    `json:"warnings"`
}

// SearchReleases busca ediciones en todas las fuentes (exacta y aproximada).
func (a *App) SearchReleases(artist, album string) (searchResult, error) {
	if strings.TrimSpace(artist+album) == "" {
		return searchResult{}, fmt.Errorf("indica artista o álbum")
	}
	results, warnings := searchAll(a.ctx, artist, album, a.searchOpts(true))
	// Las más parecidas a lo que se pidió, arriba.
	sort.SliceStable(results, func(i, j int) bool {
		return nameScore(results[i], artist, album) > nameScore(results[j], artist, album)
	})
	return searchResult{Results: results, Warnings: warnings}, nil
}

// nameScore: parecido de una candidata con el artista/álbum buscados.
func nameScore(r mbRelease, artist, album string) float64 {
	s := similarity(r.Title, album)
	if artist != "" {
		s += 0.5 * similarity(r.Artist, artist)
	}
	return s
}

// GetRelease trae la tracklist de una edición, de la fuente que sea.
func (a *App) GetRelease(key string) (mbReleaseDetail, error) {
	d, err := getDetail(a.ctx, key, a.searchOpts(false).DiscogsToken)
	if err != nil {
		return d, err
	}
	a.fillLengths(a.ctx, &d) // Discogs a veces no trae duraciones: se buscan en otra fuente
	return d, nil
}

// compareResult es la comparación de una carpeta con una edición: la
// tracklist y con qué fichero se empareja cada pista.
type compareResult struct {
	Release mbReleaseDetail `json:"release"`
	Pairs   []trackPair     `json:"pairs"`
	State   string          `json:"state"`
	Matched int             `json:"matched"`
}

// CompareRelease empareja los ficheros de la carpeta con la tracklist de la
// edición por parecido de título y duración (no por orden).
func (a *App) CompareRelease(dir, key string) (compareResult, error) {
	d, err := a.GetRelease(key)
	if err != nil {
		return compareResult{}, err
	}
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return compareResult{}, err
	}
	state, matched, _ := evaluate(&d, scan)
	return compareResult{Release: d, Pairs: matchTracks(scan.Files, d.Tracks), State: state, Matched: matched}, nil
}

// FetchCover guarda cover.jpg en la carpeta y la incrusta en las pistas.
func (a *App) FetchCover(dir, key string) (int, error) {
	d, err := a.GetRelease(key)
	if err != nil {
		return 0, err
	}
	dest := filepath.Join(dir, "cover.jpg")
	a.libLog("Buscando carátula...")
	src, err := coverFor(a.ctx, d, dest, a.searchOpts(false))
	if err != nil {
		return 0, err
	}
	a.libLog("Guardada %s (de %s)", dest, src)
	n, err := a.embedCoverAll(dir, dest)
	a.autoIcon(dir, d.Release.Format)
	return n, err
}

// embedCoverAll incrusta la carátula en todos los ficheros de audio de dir.
func (a *App) embedCoverAll(dir, cover string) (int, error) {
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range scan.Files {
		if err := rewrite(a.ctx, f.Path, f.Path, nil, cover); err != nil {
			a.libLog("aviso: %s: %v", f.Name, err)
			continue
		}
		n++
	}
	a.libLog("Carátula incrustada en %d fichero(s)", n)
	return n, nil
}

// SplitFile parte un fichero largo en pistas usando las duraciones de la
// edición de MusicBrainz. El original se aparta a _original/ por si acaso.
func (a *App) SplitFile(path, key string) ([]string, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return nil, err
	}
	for _, t := range rel.Tracks {
		if t.Length == 0 {
			return nil, fmt.Errorf("la fuente no tiene la duración de la pista %d (%s); no puedo cortar con precisión", t.Number, t.Title)
		}
	}

	src, err := probe(a.ctx, path)
	if err != nil {
		return nil, err
	}
	dur := src.Duration
	if diff := dur - rel.TotalLength; math.Abs(diff) > 15 {
		a.libLog("aviso: el fichero dura %s y la edición %s (diferencia %+.0f s); los cortes pueden no cuadrar", fmtDur(dur), fmtDur(rel.TotalLength), diff)
	}

	// Cada pista empieza donde acaba la anterior.
	bounds := make([]float64, len(rel.Tracks))
	for i := 1; i < len(bounds); i++ {
		bounds[i] = bounds[i-1] + rel.Tracks[i-1].Length
	}
	return a.splitAt(path, rel, bounds)
}

// TagFolder renombra y etiqueta las pistas de la carpeta según la tracklist
// oficial. Empareja cada pista con el fichero que más se le parece (título +
// duración), así que arregla también ficheros desordenados o mal nombrados.
// Los ficheros que no casan con ninguna pista se dejan como están.
func (a *App) TagFolder(dir, key string) (int, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return 0, err
	}
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return 0, err
	}
	pairs := matchTracks(scan.Files, rel.Tracks)
	if assigned, _ := countMatched(pairs); assigned == 0 {
		return 0, fmt.Errorf("ningún fichero se parece a las pistas de esta edición")
	}

	// Primero a nombres temporales: si dos ficheros intercambian número
	// (01 <-> 02), renombrar directamente pisaría uno con el otro.
	type move struct {
		from, tmp, to string
		track         mbTrack
	}
	var moves []move
	used := make([]bool, len(scan.Files))
	for _, p := range pairs {
		if p.File < 0 {
			continue
		}
		f := scan.Files[p.File]
		t := rel.Tracks[p.Track]
		used[p.File] = true
		to := filepath.Join(dir, fmt.Sprintf("%02d - %s%s", t.Number, safeName(t.Title), filepath.Ext(f.Path)))
		moves = append(moves, move{from: f.Path, tmp: filepath.Join(dir, fmt.Sprintf(".retag-%02d%s", t.Number, filepath.Ext(f.Path))), to: to, track: t})
	}
	for i, f := range scan.Files {
		if !used[i] {
			a.libLog("aviso: %s no se parece a ninguna pista de la edición; se deja como está", f.Name)
		}
	}

	n := 0
	for _, m := range moves {
		if err := rewrite(a.ctx, m.from, m.tmp, trackTags(rel, m.track), scan.Cover); err != nil {
			a.libLog("aviso: %s: %v", filepath.Base(m.from), err)
			continue
		}
		n++
	}
	for _, m := range moves {
		if !fileExists(m.tmp) {
			continue
		}
		// Si ya hay un fichero con ese nombre (uno que no casó con ninguna
		// pista), no lo pisamos: el nuevo lleva sufijo.
		if fileExists(m.to) {
			ext := filepath.Ext(m.to)
			m.to = strings.TrimSuffix(m.to, ext) + " (2)" + ext
		}
		if err := os.Rename(m.tmp, m.to); err != nil {
			a.libLog("aviso: no pude renombrar a %s: %v", filepath.Base(m.to), err)
			continue
		}
		if !strings.EqualFold(m.from, m.to) {
			a.libLog("%s  ->  %s", filepath.Base(m.from), filepath.Base(m.to))
		}
	}
	a.libLog("Etiquetadas %d pistas", n)
	a.autoIcon(dir, rel.Release.Format)
	return n, nil
}

// ---- ffmpeg / ffprobe ----------------------------------------------------

// probe lee un fichero de audio con ffprobe: duración, etiquetas y datos
// técnicos del flujo de audio, y si lleva una imagen incrustada.
func probe(ctx context.Context, path string) (localTrack, error) {
	t := localTrack{Path: path, Name: filepath.Base(path)}
	cmd := exec.CommandContext(ctx, toolPath("ffprobe.exe"), "-v", "error",
		"-show_entries", "format=duration,bit_rate,size:format_tags"+
			":stream=codec_type,codec_name,sample_rate,channels,bit_rate:stream_disposition=attached_pic",
		"-of", "json", path)
	hideWindow(cmd)
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
	cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error",
		"-i", path, "-an", "-map", "0:v:0", "-c:v", "copy", "-f", "image2pipe", "-")
	hideWindow(cmd)
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

	cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), args...)
	hideWindow(cmd)
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
	if inputArgs == nil && !strings.EqualFold(in, out) {
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

// ---- utilidades ----------------------------------------------------------

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// safeName quita los caracteres que Windows no admite en nombres de fichero.
// Los dos puntos se convierten en " - " ("Magdalena: Medieval Songs" ->
// "Magdalena - Medieval Songs"); el resto, en guion.
func safeName(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")
	s = strings.ReplaceAll(s, ":", " -")
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '-'
		}
		return r
	}, s)
	s = spaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(strings.TrimRight(s, "."))
}

// ffTime formatea segundos como los quiere ffmpeg: "123.456".
func ffTime(sec float64) string { return strconv.FormatFloat(sec, 'f', 3, 64) }

// fmtDur: 754.3 -> "12:34".
func fmtDur(sec float64) string {
	s := int(math.Round(sec))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// naturalLess ordena "2.mp3" antes que "10.mp3": compara los tramos
// numéricos por valor y el resto sin distinguir mayúsculas.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		ca, cb := chunk(a), chunk(b)
		a, b = a[len(ca):], b[len(cb):]
		if isDigits(ca) && isDigits(cb) {
			na, _ := strconv.Atoi(ca)
			nb, _ := strconv.Atoi(cb)
			if na != nb {
				return na < nb
			}
			continue
		}
		if la, lb := strings.ToLower(ca), strings.ToLower(cb); la != lb {
			return la < lb
		}
	}
	return len(a) < len(b)
}

// chunk devuelve el primer tramo de s: todo dígitos o todo no-dígitos.
func chunk(s string) string {
	digits := unicode.IsDigit(rune(s[0]))
	for i, r := range s {
		if unicode.IsDigit(r) != digits {
			return s[:i]
		}
	}
	return s
}

func isDigits(s string) bool {
	return s != "" && unicode.IsDigit(rune(s[0]))
}

// ---- carátulas para la vista de miniaturas --------------------------------

// coverCache evita extraer la misma carátula cada vez que se redibuja la
// cuadrícula. La clave es la carpeta; se invalida si cambia el fichero
// origen (ruta + fecha de modificación).
type coverCache struct {
	mu      sync.Mutex
	entries map[string]coverEntry
}

type coverEntry struct {
	source string // ruta del fichero del que salió + mtime
	data   string // data URL
}

var covers = coverCache{entries: make(map[string]coverEntry)}

// AlbumCover devuelve la carátula de un disco como data URL: cover.jpg si
// existe; si no, la imagen incrustada en el primer fichero de audio. Cadena
// vacía si no hay ninguna.
func (a *App) AlbumCover(dir string) (string, error) {
	src, data, err := findCover(a.ctx, dir)
	if err != nil {
		return "", err
	}
	if src == "" {
		return "", nil
	}
	covers.mu.Lock()
	defer covers.mu.Unlock()
	if e, ok := covers.entries[dir]; ok && e.source == src {
		return e.data, nil
	}
	url := "data:" + sniffImageType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
	covers.entries[dir] = coverEntry{source: src, data: url}
	return url, nil
}

// findCover localiza la carátula de la carpeta y devuelve (clave de caché,
// bytes). Solo lee los bytes si hace falta (clave distinta a la cacheada).
func findCover(ctx context.Context, dir string) (string, []byte, error) {
	if p := filepath.Join(dir, "cover.jpg"); fileExists(p) {
		info, _ := os.Stat(p)
		key := p + "|" + info.ModTime().String()
		if cached(dir, key) {
			return key, nil, nil
		}
		data, err := os.ReadFile(p)
		return key, data, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil, nil
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	// El primer fichero que lleve imagen (en carpetas mixtas no todos la
	// tienen). Como mucho miramos 8 para no lanzar ffmpeg veinte veces.
	for i, name := range names {
		if i >= 8 {
			break
		}
		p := filepath.Join(dir, name)
		info, _ := os.Stat(p)
		key := p + "|" + info.ModTime().String()
		if cached(dir, key) {
			return key, nil, nil
		}
		if data, err := embeddedCover(ctx, p); err == nil {
			return key, data, nil
		}
	}
	return "", nil, nil // sin carátula: no es un error para la ventana
}

func cached(dir, key string) bool {
	covers.mu.Lock()
	defer covers.mu.Unlock()
	e, ok := covers.entries[dir]
	return ok && e.source == key
}

// sniffImageType mira los primeros bytes: JPEG empieza por FF D8, PNG por
// 89 50 4E 47.
func sniffImageType(b []byte) string {
	switch {
	case len(b) >= 4 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	default:
		return "image/jpeg"
	}
}

// commonArtist devuelve el artista del disco según las etiquetas: el
// album_artist si lo hay; si no, el artista de las pistas cuando es el mismo
// en (casi) todas. En recopilaciones con muchos artistas devuelve "" para no
// contaminar la búsqueda.
func commonArtist(files []localTrack) string {
	counts := map[string]int{}
	for _, f := range files {
		if f.AlbumArtist != "" {
			return f.AlbumArtist
		}
		if f.Artist != "" {
			counts[f.Artist]++
		}
	}
	if len(counts) >= 3 {
		return ""
	}
	best, n := "", 0
	for a, c := range counts {
		if c > n {
			best, n = a, c
		}
	}
	return best
}

// ---- tarjeta de la cuadrícula ---------------------------------------------

// albumCard es lo que la cuadrícula pide en segundo plano por cada disco:
// carátula y la deducción artista/álbum (nombre de carpeta cotejado con las
// etiquetas), para no enseñar "1998 - Under the..." cuando se sabe que es
// "Frost — Under the Hungarian Blackmoon".
type albumCard struct {
	Cover  string `json:"cover"`
	Artist string `json:"artist"` // artista y álbum tal como se muestran (etiquetas + nombre de la carpeta)
	Album  string `json:"album"`
	// Lo que dicen solo las etiquetas, para proponer el nombre de la carpeta.
	TagArtist string `json:"tagArtist"`
	TagAlbum  string `json:"tagAlbum"`
	Date      string `json:"date"`
	Media     string `json:"media"` // etiqueta media (formato de origen) de las pistas, si la llevan
	MBID      string `json:"mbid"`  // MusicBrainz Album Artist Id de las pistas, si lo llevan
}

type cardCache struct {
	mu      sync.Mutex
	entries map[string]cardEntry
}

type cardEntry struct {
	modTime string // fecha de modificación de la carpeta: si cambia, se recalcula
	card    albumCard
}

var cards = cardCache{entries: make(map[string]cardEntry)}

// AlbumCard devuelve carátula y nombres deducidos de un disco. Solo lee las
// etiquetas de los tres primeros ficheros: basta para deducir y es rápido.
func (a *App) AlbumCard(dir string) (albumCard, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return albumCard{}, err
	}
	mod := info.ModTime().String()
	if card, ok := cachedCard(dir); ok { // memoria o disco (player.go)
		return card, nil
	}

	card := albumCard{}
	// La carátula va como miniatura (200 px) y se guarda en disco: con más
	// de mil discos, la imagen entera en base64 por tarjeta no cabe.
	var thumb string
	if _, data, err := coverBytes(a.ctx, dir); err == nil && len(data) > 0 {
		thumb, card.Cover = thumbnail(a.ctx, dir, data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return card, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	var files []localTrack
	for i, name := range names {
		if i >= 3 {
			break
		}
		if t, err := probe(a.ctx, filepath.Join(dir, name)); err == nil {
			files = append(files, t)
			if card.Media == "" {
				card.Media = t.Media
			}
			if card.MBID == "" {
				card.MBID = t.ArtistMBID
			}
		}
	}
	card.Artist, card.Album = guessNames(filepath.Base(dir), files)
	// Lo que dicen las etiquetas a secas, sin mezclarlo con el nombre de la
	// carpeta: es lo que manda al proponer cómo debería llamarse.
	card.TagArtist = commonArtist(files)
	card.TagAlbum = commonTag(files, func(f localTrack) string { return f.Album })
	card.Date = commonTag(files, func(f localTrack) string { return f.Date })

	rememberCard(dir, mod, card, thumb)
	return card, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// coverBytes lee la carátula de la carpeta (cover.jpg o la incrustada en el
// primer fichero que la lleve) sin pasar por ninguna caché: (clave, bytes).
// Bytes vacíos si no hay.
func coverBytes(ctx context.Context, dir string) (string, []byte, error) {
	if p := filepath.Join(dir, "cover.jpg"); fileExists(p) {
		data, err := os.ReadFile(p)
		return p, data, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	for i, name := range names {
		if i >= 8 {
			break
		}
		p := filepath.Join(dir, name)
		if data, err := embeddedCover(ctx, p); err == nil {
			return p, data, nil
		}
	}
	return "", nil, nil
}
