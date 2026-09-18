package main

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Comparación aproximada de nombres. Los títulos de MusicBrainz y los de
// los ficheros rara vez coinciden letra a letra: acentos, mayúsculas,
// "&" vs "and", numeración delante, erratas... Aquí se mide cuánto se
// parecen dos textos (0 = nada, 1 = iguales) y se emparejan pistas.

// normalizeName deja solo letras y dígitos en minúscula, sin acentos:
// "Flögo de Bort (Demo)" -> "flogo de bort demo".
func normalizeName(s string) string {
	// NFD separa "ö" en "o" + diéresis; runes.Remove quita las marcas (Mn).
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	if out, _, err := transform.String(t, s); err == nil {
		s = out
	}
	s = strings.ToLower(s)
	s = strings.NewReplacer("&", " and ", "+", " and ").Replace(s)

	var b strings.Builder
	space := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// levenshtein: número mínimo de ediciones (insertar/borrar/sustituir) para
// convertir a en b. Programación dinámica clásica con dos filas.
func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// similarity devuelve 0..1. Combina tres medidas y se queda con la mejor:
//   - distancia de edición relativa (erratas),
//   - solapamiento de palabras (Jaccard: aguanta reordenaciones y añadidos),
//   - contención (un nombre dentro del otro: "Endzeit Barbarossa" en
//     "1941 Endzeit Barbarossa 2021").
func similarity(a, b string) float64 {
	na, nb := normalizeName(a), normalizeName(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	ra, rb := []rune(na), []rune(nb)
	longest := max(len(ra), len(rb))
	ratio := 1 - float64(levenshtein(ra, rb))/float64(longest)

	ta, tb := strings.Fields(na), strings.Fields(nb)
	set := make(map[string]bool, len(ta))
	for _, w := range ta {
		set[w] = true
	}
	inter := 0
	for _, w := range tb {
		if set[w] {
			inter++
		}
	}
	jaccard := float64(inter) / float64(len(ta)+len(tb)-inter)

	contain := 0.0
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		contain = float64(min(len(ra), len(rb))) / float64(longest)
	}
	return max(ratio, jaccard, contain)
}

var (
	leadingTrackNo = regexp.MustCompile(`^\s*\d{1,3}\s*[-._)]?\s+`)
	trailingYear   = regexp.MustCompile(`\s*[(\[]\d{4}[)\]]\s*$`)
)

// trackTitleOf es el título "útil" de un fichero para compararlo con una
// tracklist: la etiqueta si existe; si no, el nombre del fichero sin
// extensión, sin numeración delante y sin el "Artista - " si lo lleva.
func trackTitleOf(f localTrack) string {
	if strings.TrimSpace(f.Title) != "" {
		return cleanTitle(f.Title)
	}
	name := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
	name = strings.ReplaceAll(name, "_", " ") // "Artist_-_Title" -> "Artist - Title"
	name = leadingTrackNo.ReplaceAllString(name, "")
	if i := strings.LastIndex(name, " - "); i >= 0 {
		name = name[i+3:]
	}
	// El número puede ir tras el artista: "Artist - Album - 04 Title".
	name = leadingTrackNo.ReplaceAllString(name, "")
	return cleanTitle(name)
}

// trackPair es el emparejamiento de una pista oficial con un fichero local.
type trackPair struct {
	Track int     `json:"track"` // índice en la tracklist
	File  int     `json:"file"`  // índice en los ficheros, -1 si ninguno
	Diff  float64 `json:"diff"`  // duración fichero - duración oficial (s)
	Sim   float64 `json:"sim"`   // parecido de título 0..1
}

// matchTracks empareja pistas oficiales con ficheros locales por parecido
// de título y duración, sin fiarse del orden. Asignación voraz: se ordenan
// todas las combinaciones por puntuación y se van cogiendo las mejores que
// no repitan pista ni fichero. Con las decenas de pistas de un disco, es
// más que suficiente.
func matchTracks(files []localTrack, tracks []mbTrack) []trackPair {
	type cand struct {
		t, f  int
		score float64
		diff  float64
		sim   float64
	}
	var cands []cand
	for t, tr := range tracks {
		for f, fl := range files {
			sim := similarity(trackTitleOf(fl), tr.Title)
			diff := fl.Duration - tr.Length
			dur := 0.5 // duración desconocida en MusicBrainz: ni suma ni resta
			if tr.Length > 0 {
				// 1 hasta ±3 s, y baja linealmente hasta 0 en ±60 s.
				dur = math.Max(0, 1-math.Max(0, math.Abs(diff)-3)/57)
			}
			cands = append(cands, cand{t, f, 0.6*dur + 0.4*sim, diff, sim})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	pairs := make([]trackPair, len(tracks))
	for t := range pairs {
		pairs[t] = trackPair{Track: t, File: -1}
	}
	usedFile := make([]bool, len(files))
	for _, c := range cands {
		if c.score < 0.35 || pairs[c.t].File >= 0 || usedFile[c.f] {
			continue
		}
		pairs[c.t] = trackPair{Track: c.t, File: c.f, Diff: c.diff, Sim: c.sim}
		usedFile[c.f] = true
	}
	return pairs
}

// countMatched: pistas emparejadas cuya duración cuadra (±3 s).
func countMatched(pairs []trackPair) (assigned, matched int) {
	for _, p := range pairs {
		if p.File < 0 {
			continue
		}
		assigned++
		if math.Abs(p.Diff) <= 3 {
			matched++
		}
	}
	return
}

// artistSep: separador entre "Artista" y "Álbum" en un título ("Oliphant -
// Songs...", "Oliphant: Songs...").
var artistSep = regexp.MustCompile(`\s*[-–—:]\s+`)

// stripArtistPrefix quita del título del disco el "Artista - " que a veces
// viene delante (una o más veces: "Oliphant - Oliphant - Songs from the
// Crusades" vale por "Songs from the Crusades"). Solo si lo que hay delante
// es de verdad uno de los artistas dados.
func stripArtistPrefix(album string, artists ...string) string {
	s := strings.TrimSpace(album)
	for {
		loc := artistSep.FindStringIndex(s)
		if loc == nil || loc[0] == 0 {
			return s
		}
		head := normalizeName(s[:loc[0]])
		found := false
		for _, ar := range artists {
			if na := normalizeName(ar); na != "" && na == head {
				found = true
				break
			}
		}
		if !found || len(strings.TrimSpace(s[loc[1]:])) == 0 {
			return s
		}
		s = strings.TrimSpace(s[loc[1]:])
	}
}

// albumCore: lo que identifica un disco al comparar dos carpetas, sin el
// artista delante ni el año detrás.
func albumCore(album string, artists ...string) string {
	return strings.TrimSpace(trailingYear.ReplaceAllString(stripArtistPrefix(album, artists...), ""))
}
