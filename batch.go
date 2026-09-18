package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Biblioteca en lote: detectar los discos de una carpeta, analizarlos todos
// contra MusicBrainz (eligiendo sola la edición que cuadra) y aplicar
// carátula / separar / etiquetar a varios de golpe.

// albumInfo es una fila de la tabla de discos.
type albumInfo struct {
	Dir        string          `json:"dir"`
	Name       string          `json:"name"`
	Files      int             `json:"files"`
	Total      float64         `json:"total"` // segundos (0 hasta que se analiza)
	HasCover   bool            `json:"hasCover"`
	State      string          `json:"state"` // pending | analyzing | ok | splittable | mismatch | notfound | working | error
	Message    string          `json:"message"`
	Release    *mbRelease      `json:"release,omitempty"` // edición propuesta o elegida
	Matched    int             `json:"matched"`           // pistas que cuadran en duración
	Confirmed  bool            `json:"confirmed"`         // el usuario ha aceptado la edición
	Candidates []candidateInfo `json:"candidates"`        // todo lo que encontró la búsqueda
}

// candidateInfo es una edición encontrada, evaluada o no contra los ficheros.
type candidateInfo struct {
	Release   mbRelease `json:"release"`
	Evaluated bool      `json:"evaluated"`
	State     string    `json:"state"`
	Matched   int       `json:"matched"`
	Score     float64   `json:"score"`
}

// albumAnalysis es lo que guardamos en memoria por carpeta analizada.
type albumAnalysis struct {
	info    albumInfo
	scan    folderScan
	detail  *mbReleaseDetail            // edición propuesta/elegida
	details map[string]*mbReleaseDetail // caché de tracklists ya bajadas
}

// batchState vive dentro de App (campo `batch`). Un lote a la vez.
type batchState struct {
	mu      sync.Mutex
	albums  map[string]*albumAnalysis
	running bool
	cancel  context.CancelFunc
}

func (b *batchState) get(dir string) *albumAnalysis {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.albums[dir]
}

func (b *batchState) put(an *albumAnalysis) {
	b.mu.Lock()
	if b.albums == nil {
		b.albums = make(map[string]*albumAnalysis)
	}
	b.albums[an.info.Dir] = an
	b.mu.Unlock()
}

// start reserva el lote y devuelve el contexto; falla si ya hay uno.
func (b *batchState) start(parent context.Context) (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return nil, fmt.Errorf("ya hay un proceso en marcha")
	}
	ctx, cancel := context.WithCancel(parent)
	b.running, b.cancel = true, cancel
	return ctx, nil
}

func (b *batchState) finish() {
	b.mu.Lock()
	b.running = false
	b.cancel = nil
	b.mu.Unlock()
}

// ---- Métodos expuestos a la ventana --------------------------------------

// ListAlbums detecta los discos: toda carpeta con ficheros de audio que
// cuelgue de root, a cualquier profundidad (ColecciónArtistaDisco), más
// la propia carpeta si tiene audio suelto. El nombre es la ruta relativa
// ("ArtistaDisco") para saber dónde vive cada uno. Es rápido: no lee
// duraciones; eso lo hace el análisis.
func (a *App) ListAlbums(root string) ([]albumInfo, error) {
	// Se recuerda qué carpeta se está mirando: es lo que distingue un disco
	// suelto en la raíz de uno que cuelga de la carpeta de su artista, y de
	// eso depende cómo se llama la carpeta (ver albumFolderIn).
	a.mu.Lock()
	a.libRoot = filepath.Clean(root)
	a.mu.Unlock()
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // aún no se ha descargado nada: carpeta vacía
		}
		return nil, err
	}
	var out []albumInfo
	if n := countAudio(root); n > 0 {
		out = append(out, a.albumRow(root, "(ficheros sueltos)", n))
	}
	dirs, err := albumDirs(a.ctx, root)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		out = append(out, a.albumRow(dir, relTo(root, dir), countAudio(dir)))
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
	a.libLog("Biblioteca: %d discos en %s", len(out), root)
	return out, nil
}

// albumRow construye la fila, reutilizando el análisis previo si lo hay.
func (a *App) albumRow(dir, name string, files int) albumInfo {
	if an := a.batch.get(dir); an != nil {
		an.info.Files = files
		an.info.HasCover = fileExists(filepath.Join(dir, "cover.jpg"))
		return an.info
	}
	return albumInfo{Dir: dir, Name: name, Files: files, State: "pending",
		HasCover: fileExists(filepath.Join(dir, "cover.jpg"))}
}

func countAudio(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			n++
		}
	}
	return n
}

// AnalyzeAlbums analiza las carpetas en segundo plano. Cada carpeta emite
// un evento "album" con su fila actualizada; al terminar, "batch".
func (a *App) AnalyzeAlbums(dirs []string) error {
	ctx, err := a.batch.start(a.ctx)
	if err != nil {
		return err
	}
	go func() {
		defer a.batch.finish()
		defer a.emit("batch", map[string]bool{"running": false})
		for _, dir := range dirs {
			if ctx.Err() != nil {
				return
			}
			a.analyzeOne(ctx, dir)
		}
	}()
	a.emit("batch", map[string]bool{"running": true})
	return nil
}

// CancelBatch detiene el análisis o la aplicación en curso.
func (a *App) CancelBatch() {
	a.batch.mu.Lock()
	if a.batch.cancel != nil {
		a.batch.cancel()
	}
	a.batch.mu.Unlock()
}

// AlbumRelease devuelve la edición elegida para una carpeta analizada (para
// que el detalle se abra ya con ella).
func (a *App) AlbumRelease(dir string) string {
	if an := a.batch.get(dir); an != nil && an.detail != nil {
		return an.detail.Release.ID
	}
	return ""
}

// ApplyAlbums aplica en lote a las carpetas indicadas lo que se pida, según
// el estado de cada una: separar solo a las separables, etiquetar solo a
// las que cuadran, carátula a las que tienen edición y no tienen cover.jpg.
func (a *App) ApplyAlbums(dirs []string, doCover, doSplit, doTag bool) error {
	ctx, err := a.batch.start(a.ctx)
	if err != nil {
		return err
	}
	go func() {
		defer a.batch.finish()
		defer a.emit("batch", map[string]bool{"running": false})
		for _, dir := range dirs {
			if ctx.Err() != nil {
				return
			}
			a.applyOne(ctx, dir, doCover, doSplit, doTag)
		}
	}()
	a.emit("batch", map[string]bool{"running": true})
	return nil
}

// ---- análisis -------------------------------------------------------------

func (a *App) analyzeOne(ctx context.Context, dir string) {
	an := &albumAnalysis{info: albumInfo{Dir: dir, Name: filepath.Base(dir), State: "analyzing"}}
	a.batch.put(an)
	a.emit("album", an.info)

	scan, err := a.ScanFolder(dir)
	if err != nil {
		a.setAlbum(an, "error", err.Error())
		return
	}
	an.scan = scan
	an.info.Files = len(scan.Files)
	an.info.Total = scan.Total
	an.info.HasCover = scan.Cover != ""
	if len(scan.Files) == 0 {
		a.setAlbum(an, "error", "sin ficheros de audio")
		return
	}

	an.details = make(map[string]*mbReleaseDetail)
	detail, matched, state, cands, err := a.findRelease(ctx, scan, an.details)
	an.info.Candidates = cands
	if err != nil {
		if ctx.Err() != nil {
			a.setAlbum(an, "pending", "cancelado")
			return
		}
		a.setAlbum(an, "error", err.Error())
		return
	}
	if detail != nil {
		an.detail = detail
		rel := detail.Release
		an.info.Release = &rel
	}
	an.info.Matched = matched
	an.info.Confirmed = false // es una propuesta hasta que el usuario la acepte
	a.setAlbum(an, state, describe(state, matched, len(scan.Files), detail))
}

func (a *App) setAlbum(an *albumAnalysis, state, msg string) {
	an.info.State, an.info.Message = state, msg
	a.emit("album", an.info)
	a.libLog("%s: %s", an.info.Name, msg)
}

func describe(state string, matched, files int, d *mbReleaseDetail) string {
	switch state {
	case "ok":
		return fmt.Sprintf("íntegro: %d pistas cuadran con %s (%s)", matched, d.Release.Title, d.Release.Date)
	case "splittable":
		return fmt.Sprintf("un solo fichero que dura lo que %s (%d pistas): separable", d.Release.Title, len(d.Tracks))
	case "single":
		return fmt.Sprintf("pista suelta de %s (%d pistas): crear álbum la lleva a su carpeta", d.Release.Title, len(d.Tracks))
	case "partial":
		return fmt.Sprintf("parcial: tus %d ficheros cuadran con %s (%d pistas); faltan %d", matched, d.Release.Title, len(d.Tracks), len(d.Tracks)-matched)
	case "extra":
		return fmt.Sprintf("completo con extras: las %d pistas de %s están; sobran %d ficheros", matched, d.Release.Title, files-matched)
	case "mismatch":
		if d == nil {
			return "sin edición que cuadre"
		}
		if len(d.Tracks) != files {
			return fmt.Sprintf("mejor candidata %s tiene %d pistas y aquí hay %d", d.Release.Title, len(d.Tracks), files)
		}
		return fmt.Sprintf("solo %d de %d duraciones cuadran con %s", matched, files, d.Release.Title)
	case "notfound":
		return "no encontrado en MusicBrainz"
	}
	return ""
}

// findRelease busca la edición que mejor cuadra con los ficheros. Prueba
// varias consultas (etiquetas, nombre de carpeta), en todas las fuentes, y
// evalúa las candidatas más parecidas hasta que una cuadra del todo.
// Devuelve además TODAS las ediciones que aparecieron (evaluadas o no) para
// enseñárselas al usuario, y guarda en `cache` las tracklists bajadas.
func (a *App) findRelease(ctx context.Context, scan folderScan, cache map[string]*mbReleaseDetail) (best *mbReleaseDetail, bestMatched int, state string, cands []candidateInfo, err error) {
	n := len(scan.Files)
	state = "notfound"
	bestScore := -1.0
	index := map[string]int{} // id -> posición en cands

	addCand := func(r mbRelease) int {
		if i, ok := index[r.ID]; ok {
			return i
		}
		index[r.ID] = len(cands)
		cands = append(cands, candidateInfo{Release: r, State: "unknown"})
		return len(cands) - 1
	}

	opts := a.searchOpts(true)
	for qi, q := range guessQueries(scan) {
		if ctx.Err() != nil {
			return nil, 0, "", cands, ctx.Err()
		}
		opts.Fuzzy = qi == 0 // la aproximada solo con la consulta más fiable
		rels, warnings := searchAll(ctx, q[0], q[1], opts)
		for _, w := range warnings {
			a.libLog("aviso: %s", w)
		}
		for _, r := range rels {
			addCand(r) // todas se enseñan, aunque no se evalúen
		}
		for _, r := range rankCandidates(rels, n, q[0], q[1]) {
			i := addCand(r)
			if cands[i].Evaluated {
				continue
			}
			d, err := a.GetRelease(r.ID)
			if err != nil {
				a.libLog("aviso: %s: %v", r.Source, err)
				continue
			}
			cache[r.ID] = &d
			st, matched, score := evaluate(&d, scan)
			c := &cands[i]
			c.Evaluated, c.State, c.Matched, c.Score = true, st, matched, score
			if score > bestScore {
				best, bestMatched, bestScore, state = &d, matched, score, st
			}
			if identified(st) {
				sortCandidates(cands)
				return best, bestMatched, state, cands, nil
			}
		}
		if best != nil && state == "mismatch" && bestMatched > 0 {
			// Ya tenemos algo razonable; no gastar más peticiones.
			break
		}
	}

	// Segundo intento: identificar por las canciones. Sirve cuando el nombre
	// de la carpeta no dice nada ("1998 - Demo") o está mal escrito.
	if !identified(state) && n > 1 && ctx.Err() == nil {
		rels, warnings := searchByTracks(ctx, commonArtist(scan.Files), scan.Files, opts)
		for _, w := range warnings {
			a.libLog("aviso: %s", w)
		}
		for i, r := range rels {
			idx := addCand(r)
			if i >= 3 || cands[idx].Evaluated || r.Hits < 2 {
				continue // solo las 3 mejores con al menos 2 títulos en común
			}
			d, err := a.GetRelease(r.ID)
			if err != nil {
				a.libLog("aviso: %s: %v", r.Source, err)
				continue
			}
			cache[r.ID] = &d
			st, matched, score := evaluate(&d, scan)
			c := &cands[idx]
			c.Evaluated, c.State, c.Matched, c.Score = true, st, matched, score
			if score > bestScore {
				best, bestMatched, bestScore, state = &d, matched, score, st
			}
			if identified(st) {
				break
			}
		}
	}
	sortCandidates(cands)
	return best, bestMatched, state, cands, nil
}

// sortCandidates: evaluadas primero (mejor puntuación arriba), luego el
// resto por la puntuación de búsqueda de su fuente.
func sortCandidates(c []candidateInfo) {
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Evaluated != c[j].Evaluated {
			return c[i].Evaluated
		}
		if c[i].Evaluated {
			return c[i].Score > c[j].Score
		}
		return c[i].Release.Score > c[j].Release.Score
	})
}

// rankCandidates ordena las ediciones a probar: primero las que tienen
// tantas pistas como ficheros (o cualquiera si hay un solo fichero), y
// dentro de ellas las que más se parecen por nombre a lo buscado. Como
// mucho 4, para no gastar peticiones en ediciones improbables.
func rankCandidates(rels []mbRelease, files int, artist, album string) []mbRelease {
	var same, other []mbRelease
	for _, r := range rels {
		// Fuentes que no dicen cuántas pistas tienen (Bandcamp) se prueban.
		if files == 1 || r.TrackCount == files || r.TrackCount == 0 {
			same = append(same, r)
		} else {
			other = append(other, r)
		}
	}
	byName := func(s []mbRelease) {
		sort.SliceStable(s, func(i, j int) bool {
			si, sj := nameScore(s[i], artist, album), nameScore(s[j], artist, album)
			if si != sj {
				return si > sj
			}
			return s[i].Score > s[j].Score
		})
	}
	byName(same)
	byName(other)
	out := same
	if len(out) > 4 {
		out = out[:4]
	}
	if len(out) == 0 && len(other) > 0 {
		out = other[:1] // para poder decir "la mejor candidata tiene N pistas"
	}
	return out
}

// evaluate compara una edición con los ficheros y devuelve estado, pistas
// que cuadran y una puntuación para elegir entre candidatas. Las pistas se
// emparejan por parecido de título y duración, no por orden.
func evaluate(d *mbReleaseDetail, scan folderScan) (state string, matched int, score float64) {
	n := len(scan.Files)
	if n == 1 && len(d.Tracks) > 1 {
		diff := math.Abs(scan.Total - d.TotalLength)
		if d.TotalLength > 0 && diff <= 15 {
			return "splittable", 1, 100 - diff
		}
		// Un solo fichero que es UNA pista del disco (un tema traído de YouTube):
		// "pista suelta". Crear álbum la lleva a su carpeta y luego se completa.
		for _, p := range matchTracks(scan.Files, d.Tracks) {
			if p.File == 0 && math.Abs(p.Diff) <= 3 && (p.Sim >= 0.5 || math.Abs(p.Diff) <= 1) {
				return "single", 1, 120 + p.Sim*20
			}
		}
		return "mismatch", 0, 10 - diff/60
	}
	pairs := matchTracks(scan.Files, d.Tracks)
	assigned, matched := countMatched(pairs)
	m := len(d.Tracks)
	switch {
	case n == m && matched == n:
		return "ok", matched, 200
	case n < m && matched == n && n >= 2:
		// Tenemos parte del disco y todo lo que tenemos cuadra: es este disco,
		// faltan pistas (Completar).
		return "partial", matched, 150 + float64(n)/float64(m)*40
	case n > m && matched == m:
		// Están todas las pistas de la edición y sobran ficheros (extras,
		// bonus, duplicados): el disco es este.
		return "extra", matched, 150
	}
	// Puntuación: pistas que cuadran pesan más; las solo emparejadas por
	// título, algo; y se penaliza que sobren o falten pistas.
	score = 50*float64(matched)/float64(max(n, len(d.Tracks))) + 5*float64(assigned-matched)/float64(max(n, 1))
	if n != len(d.Tracks) {
		score -= 1
	}
	return "mismatch", matched, score
}

var (
	bracketed  = regexp.MustCompile(`\[[^\]]*\]`)
	parenNoise = regexp.MustCompile(`(?i)\((?:[^)]*\b(?:full|official|album|demo|ep|hd|hq|lyric|video|remaster|complet|split|single|compilation|live|bootleg|reissue|rehearsal)[^)]*|\d{4})\)`)
	wordNoise  = regexp.MustCompile(`(?i)\b(?:full\s+(?:album|ep|demo)|official\s+(?:audio|video))\b`)
	leadYear   = regexp.MustCompile(`^\s*(\d{4})\s*[-.–]\s+`) // "2014 - X", "2015. X"
	spaces     = regexp.MustCompile(`\s+`)
)

// cleanTitle quita el ruido típico de los títulos de vídeo y de las
// carpetas: "[SWE] [Raw Black]", "(Full Album)", "(Demo)", "(2021)"...
func cleanTitle(s string) string {
	s = bracketed.ReplaceAllString(s, " ")
	s = parenNoise.ReplaceAllString(s, " ")
	s = wordNoise.ReplaceAllString(s, " ")
	s = spaces.ReplaceAllString(s, " ")
	return strings.Trim(s, " -–|:.")
}

// guessQueries propone pares (artista, álbum) para buscar, de más a menos
// fiable: etiquetas de los ficheros; "Artista - Álbum" del nombre de la
// carpeta; "AÑO - Álbum" (el artista, de las etiquetas... o el propio
// número, que hay grupos que se llaman así); y el nombre a secas.
func guessQueries(scan folderScan) [][2]string {
	var qs [][2]string
	add := func(artist, album string) {
		artist, album = strings.TrimSpace(artist), strings.TrimSpace(album)
		if album == "" && artist == "" {
			return
		}
		for _, q := range qs {
			if strings.EqualFold(q[0], artist) && strings.EqualFold(q[1], album) {
				return
			}
		}
		qs = append(qs, [2]string{artist, album})
	}

	tagArtist, tagAlbum := "", ""
	if len(scan.Files) > 0 {
		f := scan.Files[0]
		tagArtist = f.AlbumArtist
		if tagArtist == "" {
			tagArtist = f.Artist
		}
		tagAlbum = f.Album
	}
	if tagAlbum != "" {
		add(tagArtist, cleanTitle(tagAlbum))
	}

	// La deducción cotejada con las etiquetas (año delante, orden invertido...)
	// va primero: es la más fiable.
	gArtist, gAlbum := guessNames(filepath.Base(scan.Dir), scan.Files)
	add(gArtist, gAlbum)

	name := cleanTitle(filepath.Base(scan.Dir))
	if m := leadYear.FindStringSubmatch(name); m != nil {
		rest := cleanTitle(name[len(m[0]):])
		add(tagArtist, rest) // "2014 - Flögo de bort": el artista viene de las etiquetas
		add(m[1], rest)      // ...o el año es el nombre del grupo ("1941")
		add("", rest)
	}
	for _, sep := range []string{" - ", " – ", " | "} {
		if artist, album, ok := strings.Cut(name, sep); ok {
			add(cleanTitle(artist), cleanTitle(album))
			break
		}
	}
	add(tagArtist, name)
	add("", name)
	return qs
}

// ---- aplicación en lote ----------------------------------------------------

func (a *App) applyOne(ctx context.Context, dir string, doCover, doSplit, doTag bool) {
	an := a.batch.get(dir)
	if an == nil || an.detail == nil || !an.info.Confirmed {
		a.libLog("%s: sin edición confirmada, se omite", filepath.Base(dir))
		return
	}
	rel := an.detail.Release
	prev := an.info.State
	a.setAlbum(an, "working", "aplicando...")

	fail := func(err error) {
		if ctx.Err() != nil {
			a.setAlbum(an, prev, "cancelado")
			return
		}
		a.setAlbum(an, "error", err.Error())
	}

	if doCover && !fileExists(filepath.Join(dir, "cover.jpg")) {
		if _, err := a.FetchCover(dir, rel.ID); err != nil {
			a.libLog("%s: carátula: %v", an.info.Name, err) // no es fatal: seguimos
		}
	}
	if doSplit && prev == "splittable" {
		if _, err := a.SplitFile(an.scan.Files[0].Path, rel.ID); err != nil {
			fail(err)
			return
		}
		doTag = false // las pistas ya salen etiquetadas del corte
	}
	if prev == "single" {
		// Una pista suelta: crear (o completar) la carpeta del disco con ella.
		res, err := a.BuildAlbum(dir, rel.ID, []int{0}, true)
		if err != nil {
			fail(err)
			return
		}
		removeIfEmptyAlbum(dir)
		a.batch.move(dir, res.Dir)
		favMove(dir, res.Dir)
		a.emit("album-moved", map[string]string{"from": dir, "to": res.Dir})
		dir = res.Dir
		doTag = false
	}
	if doTag && (prev == "ok" || prev == "partial" || prev == "extra") {
		if _, err := a.TagFolder(dir, rel.ID); err != nil {
			fail(err)
			return
		}
	}

	// La carpeta pasa a llamarse como la edición (si no se llamaba ya así), solo
	// cuando la edición cuadra con lo que hay. Si no cuadra, el nombre no se toca.
	if fits(prev) {
		if newDir, err := a.renameToEdition(dir, rel); err != nil {
			a.libLog("%s: %v", an.info.Name, err)
		} else {
			dir = newDir
		}
	}
	a.autoIcon(dir, rel.Format)

	// Volver a mirar la carpeta para reflejar el resultado.
	scan, err := a.ScanFolder(dir)
	if err != nil {
		fail(err)
		return
	}
	an.scan = scan
	an.info.Files = len(scan.Files)
	an.info.Total = scan.Total
	an.info.HasCover = scan.Cover != ""
	state, matched, _ := evaluate(an.detail, scan)
	an.info.Matched = matched
	a.setAlbum(an, state, describe(state, matched, len(scan.Files), an.detail))
}

// move actualiza el análisis guardado cuando la carpeta cambia de ruta.
func (b *batchState) move(from, to string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	an, ok := b.albums[from]
	if !ok {
		return
	}
	delete(b.albums, from)
	an.info.Dir, an.info.Name = to, filepath.Base(to)
	an.scan.Dir = to
	for i := range an.scan.Files {
		an.scan.Files[i].Path = filepath.Join(to, an.scan.Files[i].Name)
	}
	b.albums[to] = an
}

// fits: estados en los que la edición identifica el disco sin duda
// (aunque falten o sobren pistas).
func fits(state string) bool {
	switch state {
	case "ok", "splittable", "partial", "extra":
		return true
	}
	return false
}

// identified: la edición identifica el contenido, aunque la carpeta no sea
// el disco (una pista suelta). Se puede confirmar; no se renombra.
func identified(state string) bool {
	return fits(state) || state == "single"
}
