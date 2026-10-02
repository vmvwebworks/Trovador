package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Nombres: limpiar títulos de vídeos y de carpetas, sacar el artista de un
// crédito, y convertir un título en un nombre de fichero válido (en Windows,
// en FAT32). Son funciones puras, sin App: se usan desde casi todos los
// demás ficheros. Lo que compara nombres por parecido está en fuzzy.go.

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

// anyParen: cualquier paréntesis o corchete con lo que lleve dentro.
var anyParen = regexp.MustCompile(`\s*[(\[][^)\]]*[)\]]`)

// yearRe: un año entre 1900 y 2099 y nada más ("1996").
var yearRe = regexp.MustCompile(`^(19|20)\d\d$`)

// Separadores que solo aparecen entre artistas distintos. "&" y "," no
// valen: están dentro de nombres de grupo (Simon & Garfunkel; Earth, Wind &
// Fire), así que solo se parten cuando la fuente ya da los artistas sueltos.
var artistSplit = regexp.MustCompile(`(?i)\s*(/|;|\|| feat\.? | ft\.? | featuring | vs\.? | with )\s*`)

// firstArtist: el primer artista de un crédito conjunto.
func firstArtist(credit string) string {
	parts := artistSplit.Split(strings.TrimSpace(credit), 2)
	return strings.TrimSpace(parts[0])
}

// artistKey normaliza para comparar nombres de carpeta de artista:
// "The Cure" y "Cure, The" son la misma.
func artistKey(name string) string {
	k := normalizeName(name)
	k = strings.TrimSuffix(k, " the")
	k = strings.TrimPrefix(k, "the ")
	return strings.TrimSpace(k)
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
	s = strings.TrimSpace(strings.TrimRight(s, "."))
	// Windows no deja crear ficheros que se llamen como un dispositivo
	// ("NUL", "CON", "COM1"...), ni siquiera con extensión ("NUL.mp3").
	stem, _, _ := strings.Cut(s, ".")
	if reservedNames[strings.ToUpper(strings.TrimSpace(stem))] {
		s = "_" + s
	}
	return s
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// checkFolderName comprueba que name sirve como nombre de una carpeta junto
// a otra: no vacío y un solo elemento (ni "..", ni con barras). safeName puede
// devolver "" (un título hecho solo de puntos), y los nombres de los planes
// vuelven de la ventana: sin esto, la carpeta destino sería la madre.
func checkFolderName(name string) error {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("nombre de carpeta no válido: «%s»", name)
	}
	return nil
}

var badFATChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

// fatName deja un nombre apto para FAT32 (el USB del coche): sin los
// caracteres prohibidos, sin punto ni espacio al final, no muy largo.
func fatName(s string) string {
	s = badFATChars.ReplaceAllString(s, "_")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ". ")
	if len(s) > 100 {
		s = strings.TrimRight(s[:100], ". ")
	}
	if s == "" {
		s = "_"
	}
	return s
}

func sortNatural(names []string) {
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
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

// dedupe elimina duplicados conservando el orden. El truco del
// map[string]struct{} es el "set" idiomático de Go: struct{} ocupa 0 bytes.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, u := range in {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func humanSize(b int64) string {
	switch {
	case b > 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b > 1<<10:
		return fmt.Sprintf("%d kB", b>>10)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
