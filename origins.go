package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Orígenes: para carpetas que no son un disco sino una selección (una
// recopilación por compositor, "ficheros sueltos"...), averiguar de qué
// disco viene cada fichero y si esa pista está íntegra.
//
// Cada fichero se busca por su etiqueta de álbum (los que comparten álbum
// se buscan una sola vez) o, si no la tiene, por el título de la canción.

type origin struct {
	File     int        `json:"file"` // índice en los ficheros de la carpeta
	Name     string     `json:"name"`
	Album    string     `json:"album"`             // lo que dicen las etiquetas
	Artist   string     `json:"artist"`            //
	Release  *mbRelease `json:"release,omitempty"` // edición de origen encontrada
	Track    *mbTrack   `json:"track,omitempty"`   // la pista dentro de ella
	Diff     float64    `json:"diff"`              // duración fichero - oficial (s)
	Sim      float64    `json:"sim"`               // parecido de título
	State    string     `json:"state"`             // ok | mismatch | notfound
	CoverURL string     `json:"coverUrl"`
	Message  string     `json:"message"`
}

// Origins lista el disco de origen de cada fichero de la carpeta.
func (a *App) Origins(dir string) ([]origin, error) {
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return nil, err
	}
	if len(scan.Files) == 0 {
		return nil, fmt.Errorf("la carpeta no tiene ficheros de audio")
	}
	opts := a.searchOpts(false)

	// Agrupar por álbum etiquetado; los que no tienen, cada uno por su cuenta.
	type group struct {
		album, artist string
		files         []int
	}
	var groups []*group
	byKey := map[string]*group{}
	for i, f := range scan.Files {
		key := normalizeName(f.Album)
		if key == "" {
			groups = append(groups, &group{files: []int{i}})
			continue
		}
		g, ok := byKey[key]
		if !ok {
			g = &group{album: f.Album}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.files = append(g.files, i)
	}

	out := make([]origin, len(scan.Files))
	for _, g := range groups {
		if a.ctx.Err() != nil {
			return out, a.ctx.Err()
		}
		var members []localTrack
		for _, i := range g.files {
			members = append(members, scan.Files[i])
		}
		artist := realArtist(commonArtist(members))

		// Candidatas: por nombre de álbum si lo hay, si no por la canción. Se
		// prueban varias formas (con y sin artista, sin paréntesis, aproximada)
		// porque las etiquetas de estas colecciones son muy irregulares.
		var cands []mbRelease
		if g.album != "" {
			for _, q := range albumQueries(artist, g.album) {
				a.libLog("origen: buscando «%s» (%s)...", q.album, q.artist)
				found, _ := searchAll(a.ctx, q.artist, q.album, searchOptions{Fuzzy: q.fuzzy, DiscogsToken: opts.DiscogsToken})
				sort.SliceStable(found, func(i, j int) bool {
					return nameScore(found[i], q.artist, q.album) > nameScore(found[j], q.artist, q.album)
				})
				if len(found) > 0 && nameScore(found[0], "", q.album) >= 0.6 {
					cands = found
					break
				}
			}
		} else {
			f := members[0]
			a.libLog("origen: buscando la canción «%s»...", trackTitleOf(f))
			cands, _ = searchByTracks(a.ctx, artist, members, opts)
			if len(cands) == 0 && artist != "" {
				cands, _ = searchByTracks(a.ctx, "", members, opts) // el "artista" puede ser el compositor
			}
		}
		if len(cands) > 3 {
			cands = cands[:3]
		}

		// Probar las candidatas hasta que las pistas cuadren.
		for _, i := range g.files {
			out[i] = origin{File: i, Name: scan.Files[i].Name, Album: scan.Files[i].Album, Artist: scan.Files[i].Artist, State: "notfound", Message: "no encontrado"}
		}
		for _, c := range cands {
			d, err := a.GetRelease(c.ID)
			if err != nil {
				continue
			}
			allOK := true
			for _, i := range g.files {
				o := locateTrack(scan.Files[i], &d)
				if out[i].State != "ok" && (o.State == "ok" || out[i].Release == nil) {
					o.File, o.Name, o.Album, o.Artist = i, scan.Files[i].Name, scan.Files[i].Album, scan.Files[i].Artist
					if g.album == "" {
						o.Message = "por la canción: " + o.Message // sin etiqueta de álbum: menos seguro
					}
					out[i] = o
				}
				if out[i].State != "ok" {
					allOK = false
				}
			}
			if allOK {
				break
			}
		}
	}
	return out, nil
}

// locateTrack busca el fichero dentro de una edición: por número de pista si
// el título es genérico ("Track 1"), y si no por parecido de título y
// duración. Devuelve el origen con estado ok (cuadra ±3 s) o mismatch.
func locateTrack(f localTrack, d *mbReleaseDetail) origin {
	rel := d.Release
	o := origin{Release: &rel, CoverURL: coverURLOf(rel), State: "mismatch"}

	var best *mbTrack
	sim := 0.0
	byNumber := false
	if num := trackNumber(f.Track); num > 0 && genericTitle.MatchString(trackTitleOf(f)) {
		for i := range d.Tracks {
			if d.Tracks[i].Number == num {
				best, byNumber = &d.Tracks[i], true
				break
			}
		}
	}
	if best == nil {
		pairs := matchTracks([]localTrack{f}, d.Tracks)
		for _, p := range pairs {
			if p.File == 0 {
				best, sim = &d.Tracks[p.Track], p.Sim
				break
			}
		}
	}
	if best == nil {
		o.Message = "no está en " + rel.Title
		return o
	}
	t := *best
	o.Track, o.Sim, o.Diff = &t, sim, f.Duration-t.Length
	switch {
	case t.Length == 0:
		o.State, o.Message = "mismatch", fmt.Sprintf("pista %d de %s, sin duración en la fuente", t.Number, rel.Title)
	case math.Abs(o.Diff) <= 3 && !byNumber && sim < 0.5:
		// Cuadra la duración pero el título no se parece: puede ser otra
		// grabación que dura lo mismo. No se da por buena sin más.
		o.State, o.Message = "doubt", fmt.Sprintf("dura como la pista %d de %s (%s) pero el título no coincide", t.Number, rel.Title, t.Title)
	case math.Abs(o.Diff) <= 3:
		o.State, o.Message = "ok", fmt.Sprintf("pista %d de %s (%s)", t.Number, rel.Title, rel.Date)
	default:
		o.Message = fmt.Sprintf("pista %d de %s, pero dura %s y la oficial %s", t.Number, rel.Title, fmtDur(f.Duration), fmtDur(t.Length))
	}
	return o
}

// trackNumber: "3" o "3/11" -> 3.
func trackNumber(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(s, "/", 2)[0]))
	return n
}

// coverURLOf: la carátula directa de la fuente o, para MusicBrainz, la de
// Cover Art Archive en tamaño miniatura.
func coverURLOf(r mbRelease) string {
	if r.CoverURL != "" {
		return r.CoverURL
	}
	if r.Source == srcMusicBrainz && r.ID != "" {
		return caaBase + "/release/" + r.ID + "/front-250"
	}
	return ""
}

// realArtist descarta los "artistas" de relleno que ponen algunos programas.
func realArtist(s string) string {
	switch normalizeName(s) {
	case "", "unknown artist", "unknown", "various artists", "various", "varios artistas", "va", "desconocido":
		return ""
	}
	return s
}

type albumQuery struct {
	artist, album string
	fuzzy         bool
}

// albumQueries: variantes de búsqueda de un álbum etiquetado, de más a
// menos estricta. "Trouvères at the Court of Champagne (Trouvères à la cour
// de...)" se prueba también solo con lo de antes del paréntesis.
func albumQueries(artist, album string) []albumQuery {
	album = strings.TrimSpace(album)
	short := album
	if i := strings.Index(album, " ("); i > 0 {
		short = strings.TrimSpace(album[:i])
	}
	var qs []albumQuery
	add := func(q albumQuery) {
		for _, x := range qs {
			if x == q {
				return
			}
		}
		qs = append(qs, q)
	}
	add(albumQuery{artist, cleanTitle(album), false})
	add(albumQuery{artist, cleanTitle(short), false})
	add(albumQuery{"", cleanTitle(short), false})
	add(albumQuery{"", cleanTitle(short), true})
	return qs
}
