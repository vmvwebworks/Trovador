package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Duplicados: varias carpetas del mismo directorio con el mismo disco. Se
// detectan por parecido del artista/álbum deducido, se compara la
// integridad de cada copia y se fusionan en una sola.

// dupeFolder es una copia dentro de un grupo de duplicados.
type dupeFolder struct {
	Dir       string  `json:"dir"`
	Name      string  `json:"name"`
	Files     int     `json:"files"`
	Total     float64 `json:"total"`   // segundos
	Bitrate   int     `json:"bitrate"` // medio, kbps
	Cover     bool    `json:"cover"`   // cover.jpg o incrustada en todas
	Tagged    int     `json:"tagged"`  // % de ficheros con título+artista+álbum
	Canonical bool    `json:"canonical"`
	State     string  `json:"state"`   // frente a la edición: ok|partial|extra|mismatch|"" si no hay edición
	Matched   int     `json:"matched"` // pistas que cuadran con la edición
	Score     float64 `json:"score"`   // para recomendar cuál conservar
	Recommend bool    `json:"recommend"`
}

// dupeGroup es un disco con varias copias.
type dupeGroup struct {
	Artist  string       `json:"artist"`
	Album   string       `json:"album"`
	Release *mbRelease   `json:"release,omitempty"` // edición identificada, si se pudo
	Folders []dupeFolder `json:"folders"`
	Union   int          `json:"union"` // pistas distintas entre todas las copias
}

// FindDuplicates agrupa las carpetas de root que parecen el mismo disco y
// evalúa cada copia. Con muchas carpetas tarda: lee las etiquetas de cada
// una (caché de tarjetas) y solo escanea a fondo las que forman grupo.
func (a *App) FindDuplicates(root string) ([]dupeGroup, error) {
	albums, err := a.ListAlbums(root)
	if err != nil {
		return nil, err
	}
	type named struct {
		dir, artist, album string
	}
	var items []named
	for i, al := range albums {
		if a.ctx.Err() != nil {
			return nil, a.ctx.Err()
		}
		if i > 0 && i%50 == 0 {
			a.libLog("duplicados: leídas %d/%d carpetas...", i, len(albums))
		}
		card, err := a.AlbumCard(al.Dir)
		if err != nil {
			continue
		}
		album := card.Album
		if album == "" {
			album = cleanTitle(al.Name)
		}
		items = append(items, named{al.Dir, card.Artist, album})
	}

	// Union-find sencillo: cada carpeta empieza en su grupo y se unen las
	// parejas que se parecen.
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if sameAlbum(items[i].artist, items[i].album, items[j].artist, items[j].album) {
				parent[find(i)] = find(j)
			}
		}
	}
	groupsIdx := map[int][]int{}
	for i := range items {
		r := find(i)
		groupsIdx[r] = append(groupsIdx[r], i)
	}

	var groups []dupeGroup
	for _, idx := range groupsIdx {
		if len(idx) < 2 {
			continue
		}
		sort.Ints(idx)
		g := dupeGroup{Artist: items[idx[0]].artist, Album: items[idx[0]].album}
		for _, i := range idx {
			if g.Artist == "" {
				g.Artist = items[i].artist
			}
		}
		a.libLog("duplicados: «%s» (%d copias)", g.Album, len(idx))

		// Escaneo a fondo de cada copia.
		var scans []folderScan
		for _, i := range idx {
			scan, err := a.ScanFolder(items[i].dir)
			if err != nil {
				continue
			}
			scans = append(scans, scan)
			g.Folders = append(g.Folders, summarize(scan))
		}
		if len(scans) < 2 {
			continue
		}
		g.Union = len(unionTracks(scans))

		// Identificar la edición (con la copia más completa) y evaluar cada una.
		best := 0
		for i, s := range scans {
			if len(s.Files) > len(scans[best].Files) {
				best = i
			}
		}
		cache := map[string]*mbReleaseDetail{}
		if detail, _, state, _, err := a.findRelease(a.ctx, scans[best], cache); err == nil && detail != nil && fits(state) {
			rel := detail.Release
			g.Release = &rel
			for i, s := range scans {
				st, matched, _ := evaluate(detail, s)
				g.Folders[i].State, g.Folders[i].Matched = st, matched
			}
		}

		// Recomendación: la copia con más puntos.
		bi := 0
		for i := range g.Folders {
			f := &g.Folders[i]
			f.Score = float64(f.Files)*2 + float64(f.Bitrate)/32 + float64(f.Tagged)/10
			if f.Cover {
				f.Score += 10
			}
			if f.Canonical {
				f.Score += 5
			}
			if fits(f.State) {
				f.Score += 100 + float64(f.Matched)*3
			}
			if f.Score > g.Folders[bi].Score {
				bi = i
			}
		}
		g.Folders[bi].Recommend = true
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return naturalLess(groups[i].Album, groups[j].Album) })
	return groups, nil
}

// sameAlbum: dos carpetas parecen el mismo disco.
func sameAlbum(artistA, albumA, artistB, albumB string) bool {
	// "Oliphant - Songs from the Crusades" y "Songs from the Crusades (1996)"
	// son el mismo disco: se compara sin artista delante ni año detrás.
	if similarity(albumCore(albumA, artistA, artistB), albumCore(albumB, artistA, artistB)) < 0.85 {
		return false
	}
	if artistA != "" && artistB != "" && similarity(artistA, artistB) < 0.7 {
		return false
	}
	return true
}

// summarize resume la integridad de una copia.
func summarize(s folderScan) dupeFolder {
	f := dupeFolder{Dir: s.Dir, Name: filepath.Base(s.Dir), Files: len(s.Files), Total: s.Total}
	if len(s.Files) == 0 {
		return f
	}
	br, tagged, covers := 0, 0, 0
	for _, t := range s.Files {
		br += t.Bitrate
		if t.Title != "" && t.Artist != "" && t.Album != "" {
			tagged++
		}
		if t.HasCover {
			covers++
		}
	}
	f.Bitrate = br / len(s.Files)
	f.Tagged = tagged * 100 / len(s.Files)
	f.Cover = s.Cover != "" || covers == len(s.Files)
	f.Canonical = canonicalRe.MatchString(f.Name)
	return f
}

// unionTracks: pistas distintas entre varias copias (por título+duración).
func unionTracks(scans []folderScan) []localTrack {
	var all []localTrack
	for _, s := range scans {
		for _, f := range s.Files {
			if !hasTrack(all, f) {
				all = append(all, f)
			}
		}
	}
	return all
}

// hasTrack: ¿hay en files una pista igual (título parecido y ±3 s)?
func hasTrack(files []localTrack, t localTrack) bool {
	for _, f := range files {
		if math.Abs(f.Duration-t.Duration) <= 3 && similarity(trackTitleOf(f), trackTitleOf(t)) >= 0.5 {
			return true
		}
	}
	return false
}

// mergeResult es lo que devuelve MergeDuplicates.
type mergeResult struct {
	Keep     string   `json:"keep"`
	Moved    int      `json:"moved"`    // pistas traídas a la copia buena
	Archived []string `json:"archived"` // carpetas apartadas a _duplicados
}

// MergeDuplicates deja una sola copia: a `keep` se le traen las pistas que
// le falten de las otras, y las otras carpetas se apartan enteras a
// <raíz>/_duplicados/ (no se borra nada).
func (a *App) MergeDuplicates(keep string, others []string) (mergeResult, error) {
	res := mergeResult{Keep: keep}
	keepScan, err := a.ScanFolder(keep)
	if err != nil {
		return res, err
	}
	archive := filepath.Join(filepath.Dir(keep), "_duplicados")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		return res, err
	}

	for _, other := range others {
		if strings.EqualFold(other, keep) {
			continue
		}
		scan, err := a.ScanFolder(other)
		if err != nil {
			a.libLog("aviso: %s: %v", filepath.Base(other), err)
			continue
		}
		for _, f := range scan.Files {
			if hasTrack(keepScan.Files, f) {
				continue // la copia buena ya la tiene
			}
			dest := filepath.Join(keep, f.Name)
			if fileExists(dest) {
				ext := filepath.Ext(dest)
				dest = strings.TrimSuffix(dest, ext) + " (2)" + ext
			}
			if err := os.Rename(f.Path, dest); err != nil {
				a.libLog("aviso: no pude mover %s: %v", f.Name, err)
				continue
			}
			a.libLog("traída a %s: %s", filepath.Base(keep), f.Name)
			f.Path = dest
			keepScan.Files = append(keepScan.Files, f)
			res.Moved++
		}
		if keepScan.Cover == "" && scan.Cover != "" {
			if err := os.Rename(scan.Cover, filepath.Join(keep, "cover.jpg")); err == nil {
				keepScan.Cover = filepath.Join(keep, "cover.jpg")
				a.libLog("carátula traída de %s", filepath.Base(other))
			}
		}

		dest := filepath.Join(archive, filepath.Base(other))
		for i := 2; fileExists(dest); i++ {
			dest = filepath.Join(archive, fmt.Sprintf("%s (%d)", filepath.Base(other), i))
		}
		if err := os.Rename(other, dest); err != nil {
			a.libLog("aviso: no pude apartar %s: %v", filepath.Base(other), err)
			continue
		}
		a.batch.move(other, dest)
		res.Archived = append(res.Archived, dest)
		a.libLog("apartada a _duplicados: %s", filepath.Base(other))
	}
	a.emit("album", albumInfo{Dir: keep, Name: filepath.Base(keep), Files: len(keepScan.Files), State: "pending"})
	return res, nil
}

// canonicalRe: "Artista - Álbum (Año)", el nombre que genera la app.
var canonicalRe = regexp.MustCompile(`^.+ - .+ \((19|20)\d\d\)$`)
