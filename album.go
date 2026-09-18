package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Generar un álbum a partir de la información de una edición: crear su
// carpeta, llevarse los ficheros que le pertenecen ya curados (nombre,
// etiquetas, carátula), y completar las pistas que falten buscándolas en
// YouTube.

// buildResult es lo que devuelve BuildAlbum a la ventana.
type buildResult struct {
	Dir     string    `json:"dir"`     // carpeta creada
	Moved   int       `json:"moved"`   // ficheros llevados
	Missing []mbTrack `json:"missing"` // pistas de la edición que aún no están
}

// albumTitle: el título de la edición a secas, sin el año que a veces le
// viene pegado delante ("2014 - Nocturnal Poisoning").
func albumTitle(r mbRelease) string {
	title := strings.TrimSpace(r.Title)
	if m := leadYear.FindStringSubmatch(title); m != nil {
		title = strings.TrimSpace(title[len(m[0]):])
	}
	return title
}

// albumFolderIn: cómo se llama la carpeta de un disco según de dónde
// cuelgue. Dentro de la carpeta de su artista basta el título: el artista
// ya lo dice la carpeta madre y el año está en las etiquetas, así que
// repetirlos solo alarga el nombre. Suelta —la carpeta de descargas es
// plana— lleva "Artista - Álbum (Año)" para que se explique sola.
func (a *App) albumFolderIn(parent string, r mbRelease) string {
	if a.isArtistFolder(parent, r) {
		if title := safeName(albumTitle(r)); title != "" {
			return title
		}
	}
	return albumFolderName(r)
}

// isArtistFolder: esa carpeta madre es la de un artista y no el sitio llano
// donde se amontonan los discos. Manda la estructura, no el nombre: la
// carpeta del artista puede llamarse cualquier cosa y no tiene por qué
// coincidir con lo que digan las etiquetas ("anthopophobia" con discos de
// Wolfkhan dentro). Si cuelga de la carpeta que se está mirando en la
// Biblioteca, es de artista; la raíz misma, no.
func (a *App) isArtistFolder(parent string, r mbRelease) bool {
	a.mu.Lock()
	root := a.libRoot
	a.mu.Unlock()
	parent = filepath.Clean(parent)
	if root != "" && insideOf(parent, root) && !strings.EqualFold(parent, filepath.Clean(root)) {
		return true
	}
	// Sin raíz conocida (nada más arrancar), queda el nombre: si la carpeta
	// madre se llama como el artista, es la suya.
	if r.Artist == "" {
		return false
	}
	p := artistKey(filepath.Base(parent))
	return p == artistKey(r.Artist) || p == artistKey(firstArtist(r.Artist))
}

// albumFolderName: "Artista - Álbum (Año)" apto para Windows, para una
// carpeta suelta. Algunas fuentes ya meten el artista o el año en el título
// ("Oliphant - Songs from the Crusades", "1992 - Le Chant des troubadours"):
// no se repiten.
func albumFolderName(r mbRelease) string {
	title := strings.TrimSpace(r.Title)
	year := yearOf(r.Date)
	if m := leadYear.FindStringSubmatch(title); m != nil {
		if year == "" {
			year = m[1]
		}
		title = strings.TrimSpace(title[len(m[0]):])
	}
	name := title
	if r.Artist != "" && !strings.HasPrefix(normalizeName(title), normalizeName(r.Artist)+" ") {
		name = r.Artist + " - " + title
	}
	if year != "" {
		name += " (" + year + ")"
	}
	return safeName(name)
}

// BuildAlbum crea la carpeta del álbum junto a la carpeta actual y lleva
// allí los ficheros indicados, curados: "NN - Título.ext", etiquetas de la
// edición y carátula incrustada. Con move=false los copia en vez de moverlos.
func (a *App) BuildAlbum(srcDir, key string, fileIdx []int, move bool) (buildResult, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return buildResult{}, err
	}
	scan, err := a.ScanFolder(srcDir)
	if err != nil {
		return buildResult{}, err
	}

	dir := filepath.Join(filepath.Dir(srcDir), a.albumFolderIn(filepath.Dir(srcDir), rel.Release))
	if strings.EqualFold(dir, srcDir) {
		return buildResult{}, fmt.Errorf("la carpeta ya se llama así")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return buildResult{}, err
	}
	a.libLog("álbum: %s", dir)

	cover := filepath.Join(dir, "cover.jpg")
	if !fileExists(cover) {
		if src, err := coverFor(a.ctx, rel, cover, a.searchOpts(false)); err != nil {
			a.libLog("aviso: carátula: %v", err)
			cover = ""
		} else {
			a.libLog("carátula de %s", src)
		}
	}

	res := buildResult{Dir: dir}
	for _, i := range fileIdx {
		if i < 0 || i >= len(scan.Files) {
			continue
		}
		f := scan.Files[i]
		o := locateTrack(f, &rel)
		if o.Track == nil {
			a.libLog("aviso: %s no casa con ninguna pista de %s; se queda", f.Name, rel.Release.Title)
			continue
		}
		out := filepath.Join(dir, fmt.Sprintf("%02d - %s%s", o.Track.Number, safeName(o.Track.Title), filepath.Ext(f.Path)))
		if fileExists(out) {
			ext := filepath.Ext(out)
			out = strings.TrimSuffix(out, ext) + " (2)" + ext
		}
		// inputArgs vacío pero no nil: rewriteArgs conserva el original (copia);
		// nil lo elimina (mover).
		var keep []string
		if !move {
			keep = []string{}
		}
		if err := rewriteArgs(a.ctx, f.Path, out, keep, trackTags(rel, *o.Track), cover); err != nil {
			a.libLog("aviso: %s: %v", f.Name, err)
			continue
		}
		a.libLog("%s  ->  %s", f.Name, filepath.Base(out))
		res.Moved++
	}

	res.Missing, err = a.missingTracks(dir, rel)
	a.autoIcon(dir, rel.Release.Format)
	return res, err
}

// MissingTracks: pistas de la edición que no están en la carpeta del álbum.
func (a *App) MissingTracks(albumDir, key string) ([]mbTrack, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return nil, err
	}
	return a.missingTracks(albumDir, rel)
}

func (a *App) missingTracks(albumDir string, rel mbReleaseDetail) ([]mbTrack, error) {
	scan, err := a.ScanFolder(albumDir)
	if err != nil {
		return nil, err
	}
	have := map[int]bool{}
	for _, p := range matchTracks(scan.Files, rel.Tracks) {
		if p.File >= 0 && math.Abs(p.Diff) <= 10 { // algo más laxo: otra grabación de YouTube
			have[p.Track] = true
		}
	}
	var missing []mbTrack
	for i, t := range rel.Tracks {
		if !have[i] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

// trackSource es un candidato para una pista que falta: un vídeo de YouTube
// o un fichero de un usuario de Soulseek.
type trackSource struct {
	Source   string  `json:"source"` // youtube | soulseek
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
	Diff     float64 `json:"diff"` // candidato - oficial (s)
	Sim      float64 `json:"sim"`  // parecido del título
	Score    float64 `json:"score"`
	// YouTube
	URL     string `json:"url"`
	Channel string `json:"channel"`
	// Soulseek
	Username string `json:"username"`
	Filename string `json:"filename"` // ruta remota
	Folder   string `json:"folder"`
	Size     int64  `json:"size"`
	BitRate  int    `json:"bitRate"`
	Ext      string `json:"ext"`
	FreeSlot bool   `json:"freeSlot"`
	Queue    int    `json:"queue"`
}

// FindTrackSources busca en YouTube candidatos para una pista y los ordena
// por duración (lo que más fiabilidad da) y parecido de título.
func (a *App) FindTrackSources(artist, title string, length float64) ([]trackSource, error) {
	query := strings.TrimSpace(artist + " " + title)
	cmd := exec.CommandContext(a.ctx, toolPath("yt-dlp.exe"), "-J", "--flat-playlist", "--no-warnings", "ytsearch8:"+query)
	hideWindow(cmd)
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("búsqueda en YouTube: %w", err)
	}
	var raw struct {
		Entries []struct {
			URL      string  `json:"url"`
			Title    string  `json:"title"`
			Channel  string  `json:"channel"`
			Uploader string  `json:"uploader"`
			Duration float64 `json:"duration"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	var res []trackSource
	for _, e := range raw.Entries {
		if e.URL == "" || e.Duration == 0 {
			continue
		}
		ch := e.Channel
		if ch == "" {
			ch = e.Uploader
		}
		res = append(res, trackSource{
			Source: "youtube", URL: e.URL, Title: e.Title, Channel: ch, Duration: e.Duration,
			Diff: e.Duration - length, Sim: similarity(e.Title, title),
		})
	}
	score := func(s trackSource) float64 {
		dur := 0.5
		if length > 0 {
			dur = math.Max(0, 1-math.Max(0, math.Abs(s.Diff)-3)/57)
		}
		bonus := 0.0
		if artist != "" && similarity(s.Channel, artist) > 0.8 {
			bonus = 0.15 // el canal del propio artista
		}
		return 0.6*dur + 0.4*s.Sim + bonus
	}
	for i := range res {
		res[i].Score = score(res[i])
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].Score > res[j].Score })
	return res, nil
}

// DownloadTrack baja un vídeo a la carpeta del álbum y lo deja curado como
// la pista `number` de la edición: nombre, etiquetas y carátula del álbum.
func (a *App) DownloadTrack(url, albumDir, key string, number int) (string, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return "", err
	}
	var track *mbTrack
	for i := range rel.Tracks {
		if rel.Tracks[i].Number == number {
			track = &rel.Tracks[i]
		}
	}
	if track == nil {
		return "", fmt.Errorf("la edición no tiene pista %d", number)
	}

	a.mu.Lock()
	cfg := config{outDir: albumDir, workers: 1, format: a.settings.Format, quality: a.settings.Quality}
	a.mu.Unlock()
	if cfg.format == "" {
		cfg.format, cfg.quality = "mp3", "0"
	}

	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.libLog("descargando pista %d (%s) desde %s", number, track.Title, url)
	finals, err := download(ctx, number, url, cfg, func(line string) {
		if strings.Contains(line, "download:") || strings.Contains(line, "Destination") {
			return // el progreso línea a línea sobra en este registro
		}
		a.libLog("%s", line)
	})
	if err != nil {
		return "", err
	}
	if len(finals) == 0 {
		return "", fmt.Errorf("yt-dlp no produjo ningún fichero (¿ya estaba en el registro de descargas?)")
	}
	src := finals[len(finals)-1]

	out := filepath.Join(albumDir, fmt.Sprintf("%02d - %s%s", track.Number, safeName(track.Title), filepath.Ext(src)))
	cover := filepath.Join(albumDir, "cover.jpg")
	if !fileExists(cover) {
		cover = ""
	}
	if err := rewrite(a.ctx, src, out, trackTags(rel, *track), cover); err != nil {
		return "", err
	}
	a.libLog("pista %d lista: %s", number, filepath.Base(out))
	return out, nil
}

// ---- renombrar la carpeta al nombre canónico -------------------------------

// renameToEdition renombra la carpeta del disco a "Artista - Álbum (Año)"
// según la edición confirmada. Devuelve la ruta nueva (la misma si ya se
// llamaba así). Si ya existe otra carpeta con ese nombre no la mezcla: avisa
// y deja la actual como está.
func (a *App) renameToEdition(dir string, rel mbRelease) (string, error) {
	return a.renameAlbumTo(dir, a.albumFolderIn(filepath.Dir(dir), rel))
}

// renameAlbumTo renombra la carpeta del disco, dejándola donde está. Es el
// único sitio que mueve la carpeta por su nombre: lo usan el renombrado por
// edición y el renombrado en lote.
func (a *App) renameAlbumTo(dir, name string) (string, error) {
	target := filepath.Join(filepath.Dir(dir), name)
	if strings.EqualFold(filepath.Clean(target), filepath.Clean(dir)) {
		return dir, nil
	}
	if fileExists(target) {
		return dir, fmt.Errorf("ya existe una carpeta «%s»; no la mezclo", filepath.Base(target))
	}
	if err := os.Rename(dir, target); err != nil {
		return dir, fmt.Errorf("no pude renombrar la carpeta (¿algún fichero abierto?): %w", err)
	}
	a.batch.move(dir, target)
	a.libLog("carpeta renombrada: %s  ->  %s", filepath.Base(dir), filepath.Base(target))
	favMove(dir, target)
	a.emit("album-moved", map[string]string{"from": dir, "to": target})
	return target, nil
}

// RenameToEdition: versión para la ventana, por clave de edición. Solo
// renombra si la edición cuadra con lo que hay en la carpeta (íntegro,
// parcial, con extras o separable); si no, devuelve la ruta sin tocar.
func (a *App) RenameToEdition(dir, key string) (string, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return dir, err
	}
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return dir, err
	}
	if state, _, _ := evaluate(&rel, scan); !fits(state) {
		return dir, nil
	}
	return a.renameToEdition(dir, rel.Release)
}

// yearOf saca el año de una fecha "2004-01-01" / "2004"; "" si no hay año
// real (Deezer devuelve "0000-00-00" cuando no lo sabe).
func yearOf(date string) string {
	if len(date) < 4 {
		return ""
	}
	if y := date[:4]; yearRe.MatchString(y) {
		return y
	}
	return ""
}

// removeIfEmptyAlbum quita una carpeta de disco que se ha quedado sin audio
// (tras llevarse su única pista a su álbum). Una cover.jpg huérfana no la
// salva; cualquier otra cosa sí (no se borra nada que no sea nuestro).
func removeIfEmptyAlbum(dir string) {
	if countAudio(dir) > 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// Solo quedan cosas nuestras: la carátula y el icono de carpeta.
	leftovers := map[string]bool{"cover.jpg": true, iniFileName: true, iconFileName: true}
	for _, e := range entries {
		if !leftovers[strings.ToLower(e.Name())] {
			return
		}
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		_ = setAttrs(p, 0, attrHidden|attrSystem|attrReadonly)
		os.Remove(p)
	}
	_ = setAttrs(dir, 0, attrReadonly)
	os.Remove(dir)
}

// RemoveIfEmptyAlbum: versión para la ventana de removeIfEmptyAlbum.
func (a *App) RemoveIfEmptyAlbum(dir string) { removeIfEmptyAlbum(dir) }
