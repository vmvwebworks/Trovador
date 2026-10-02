package main

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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

var leadYearBare = regexp.MustCompile(`^\s*(19|20)\d\d\s+`)

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
		if !samePath(m.from, m.to) {
			a.libLog("%s  ->  %s", filepath.Base(m.from), filepath.Base(m.to))
		}
	}
	a.libLog("Etiquetadas %d pistas", n)
	a.autoIcon(dir, rel.Release.Format)
	return n, nil
}
