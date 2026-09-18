package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

// Ediciones sin duraciones (Discogs a menudo no las tiene) y cómo separar
// un fichero largo aun así: primero se intentan las duraciones de otra
// fuente que tenga el mismo disco; si no, se corta por silencios o por las
// marcas de tiempo que pegue el usuario.

// lengthsCache recuerda por edición qué duraciones se encontraron fuera
// (nil = ya se buscó y no había), para no repetir la búsqueda en cada
// comparación.
type lengthsCache struct {
	mu sync.Mutex
	m  map[string][]float64
}

// fillLengths completa las duraciones de una edición que no las trae con
// las de otra fuente que tenga el mismo disco, emparejando pistas por
// título. Deja en Release.LengthsFrom de dónde salieron.
func (a *App) fillLengths(ctx context.Context, d *mbReleaseDetail) {
	if d.TotalLength > 0 || len(d.Tracks) == 0 {
		return
	}
	key := d.Release.ID
	a.lengths.mu.Lock()
	if a.lengths.m == nil {
		a.lengths.m = map[string][]float64{}
	}
	got, seen := a.lengths.m[key]
	a.lengths.mu.Unlock()
	if seen {
		applyLengths(d, got)
		return
	}
	opts := a.searchOpts(false)
	rels, _ := searchAll(ctx, d.Release.Artist, stripParen(d.Release.Title), opts)
	n := len(d.Tracks)
	sort.SliceStable(rels, func(i, j int) bool { // mismas pistas primero
		return (rels[i].TrackCount == n) && (rels[j].TrackCount != n)
	})
	tries := 0
	var found []float64
	for _, r := range rels {
		if r.ID == key || r.CoverURL == "" && r.Source == srcDiscogs {
			continue // la misma, o Discogs sin ficha completa
		}
		if similarity(r.Title, d.Release.Title) < 0.8 || (d.Release.Artist != "" && r.Artist != "" && similarity(r.Artist, d.Release.Artist) < 0.7) {
			continue
		}
		if r.TrackCount != 0 && r.TrackCount != n {
			continue
		}
		if tries++; tries > 4 || ctx.Err() != nil {
			break
		}
		other, err := getDetail(ctx, r.ID, opts.DiscogsToken)
		if err != nil || other.TotalLength == 0 {
			continue
		}
		if lengths := matchLengths(d.Tracks, other.Tracks); lengths != nil {
			found = lengths
			d.Release.LengthsFrom = r.Source
			a.libLog("%s: duraciones tomadas de %s (%s)", d.Release.Title, r.Source, r.Title)
			break
		}
	}
	a.lengths.mu.Lock()
	a.lengths.m[key] = found
	a.lengths.mu.Unlock()
	applyLengths(d, found)
}

func applyLengths(d *mbReleaseDetail, lengths []float64) {
	if len(lengths) != len(d.Tracks) {
		return
	}
	d.TotalLength = 0
	for i := range d.Tracks {
		d.Tracks[i].Length = lengths[i]
		d.TotalLength += lengths[i]
	}
}

// matchLengths empareja las pistas de la edición sin duraciones con las
// de otra por parecido de título y devuelve las duraciones en el orden de
// la primera. Con el mismo número de pistas, las que no casen por título
// se toman por posición; si no, hace falta casar al menos el 80 %.
func matchLengths(tracks, other []mbTrack) []float64 {
	type cand struct {
		t, o int
		sim  float64
	}
	var cands []cand
	for t := range tracks {
		for o := range other {
			if s := similarity(tracks[t].Title, other[o].Title); s >= 0.6 {
				cands = append(cands, cand{t, o, s})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	assigned := make([]int, len(tracks))
	for i := range assigned {
		assigned[i] = -1
	}
	used := make([]bool, len(other))
	matched := 0
	for _, c := range cands {
		if assigned[c.t] >= 0 || used[c.o] {
			continue
		}
		assigned[c.t], used[c.o] = c.o, true
		matched++
	}
	same := len(tracks) == len(other)
	// Si solo queda una pista suelta a cada lado, son la misma ("Introduction"
	// frente a "Intro" no llega al umbral, pero no hay otra opción).
	if same && matched == len(tracks)-1 {
		for t := range assigned {
			if assigned[t] >= 0 {
				continue
			}
			for o := range used {
				if !used[o] {
					assigned[t], used[o] = o, true
					matched++
				}
			}
		}
	}
	if !same && matched*5 < len(tracks)*4 {
		return nil
	}
	if same && matched*2 < len(tracks) { // ni la mitad por título: no es el mismo tracklist
		return nil
	}
	out := make([]float64, len(tracks))
	for t := range tracks {
		o := assigned[t]
		if o < 0 && same && !used[t] {
			o = t // por posición
		}
		if o < 0 || other[o].Length <= 0 {
			return nil
		}
		out[t] = other[o].Length
	}
	return out
}

// ---- cortar sin duraciones ---------------------------------------------

var silenceRe = regexp.MustCompile(`silence_(start|end): ([0-9.]+)`)

// DetectSplits propone dónde cortar un fichero largo en n pistas: busca los
// silencios y se queda con los n-1 más largos, como marcas (segundos en
// que empieza cada pista a partir de la segunda). Si no hay bastantes,
// devuelve los que haya: el usuario los completa a mano.
func (a *App) DetectSplits(path string, n int) ([]float64, error) {
	if n < 2 {
		return nil, fmt.Errorf("hacen falta al menos 2 pistas")
	}
	src, err := probe(a.ctx, path)
	if err != nil {
		return nil, err
	}
	type sil struct{ start, end float64 }
	detect := func(noise string, minDur float64) ([]sil, error) {
		cmd := exec.CommandContext(a.ctx, toolPath("ffmpeg.exe"), "-v", "info", "-nostats", "-i", path,
			"-af", fmt.Sprintf("silencedetect=noise=%s:d=%.2f", noise, minDur), "-f", "null", "-")
		hideWindow(cmd)
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		var out []sil
		var cur float64 = -1
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			for _, m := range silenceRe.FindAllStringSubmatch(sc.Text(), -1) {
				v, _ := strconv.ParseFloat(m[2], 64)
				if m[1] == "start" {
					cur = v
				} else if cur >= 0 {
					out = append(out, sil{cur, v})
					cur = -1
				}
			}
		}
		_ = cmd.Wait() // ffmpeg devuelve 0 aunque no haya silencios
		return out, nil
	}
	// De más exigente a menos: primero silencios claros de 1,5 s; si no
	// llegan, más cortos y con más ruido admitido.
	var best []sil
	for _, try := range []struct {
		noise string
		d     float64
	}{{"-35dB", 1.5}, {"-30dB", 0.8}, {"-25dB", 0.4}} {
		sils, err := detect(try.noise, try.d)
		if err != nil {
			return nil, err
		}
		// Ni al principio ni al final, ni que dejen pistas de menos de 20 s.
		var ok []sil
		for _, s := range sils {
			if s.start > 20 && s.end < src.Duration-20 {
				ok = append(ok, s)
			}
		}
		if len(ok) > len(best) {
			best = ok
		}
		if len(best) >= n-1 {
			break
		}
	}
	sort.SliceStable(best, func(i, j int) bool { return best[i].end-best[i].start > best[j].end-best[j].start })
	var marks []float64
	for _, s := range best {
		m := s.end - 0.2 // la pista empieza donde acaba el silencio (con un pelo de margen)
		tooClose := false
		for _, x := range marks {
			if math.Abs(x-m) < 20 {
				tooClose = true
			}
		}
		if !tooClose {
			marks = append(marks, m)
		}
		if len(marks) == n-1 {
			break
		}
	}
	sort.Float64s(marks)
	if len(marks) < n-1 {
		a.libLog("silencios: solo encuentro %d cortes claros para %d pistas; completa las marcas a mano", len(marks), n)
	}
	return marks, nil
}

// SplitFileAt corta el fichero en las marcas dadas (inicio de cada pista a
// partir de la segunda, en segundos) con los títulos de la edición. Vale
// para ediciones sin duraciones: marcas de silencios o pegadas a mano.
func (a *App) SplitFileAt(path, key string, marks []float64) ([]string, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return nil, err
	}
	return a.splitMarks(path, rel, marks)
}

func (a *App) splitMarks(path string, rel mbReleaseDetail, marks []float64) ([]string, error) {
	n := len(rel.Tracks)
	if len(marks) != n-1 {
		return nil, fmt.Errorf("la edición tiene %d pistas: hacen falta %d marcas (hay %d)", n, n-1, len(marks))
	}
	src, err := probe(a.ctx, path)
	if err != nil {
		return nil, err
	}
	bounds := append([]float64{0}, marks...)
	for i := 1; i < len(bounds); i++ {
		if bounds[i] <= bounds[i-1]+1 || bounds[i] >= src.Duration {
			return nil, fmt.Errorf("marca %d (%s) fuera de orden o del fichero", i, fmtDur(bounds[i]))
		}
	}
	// Las duraciones que salen de las marcas se quedan en la edición para
	// las etiquetas y para que la comparación posterior cuadre.
	for i := range rel.Tracks {
		end := src.Duration
		if i+1 < len(bounds) {
			end = bounds[i+1]
		}
		rel.Tracks[i].Length = end - bounds[i]
	}
	rel.TotalLength = src.Duration
	return a.splitAt(path, rel, bounds)
}

// splitAt corta path en las pistas de rel empezando cada una en bounds[i].
func (a *App) splitAt(path string, rel mbReleaseDetail, bounds []float64) ([]string, error) {
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	cover := filepath.Join(dir, "cover.jpg")
	if !fileExists(cover) {
		cover = ""
	}
	var outs []string
	for i, t := range rel.Tracks {
		out := filepath.Join(dir, fmt.Sprintf("%02d - %s%s", t.Number, safeName(t.Title), ext))
		a.libLog("Pista %d/%d: %s (desde %s)", t.Number, len(rel.Tracks), t.Title, fmtDur(bounds[i]))
		args := []string{"-ss", ffTime(bounds[i])}
		if i+1 < len(bounds) { // la última va hasta el final del fichero
			args = append(args, "-t", ffTime(bounds[i+1]-bounds[i]))
		}
		if err := rewriteArgs(a.ctx, path, out, args, trackTags(rel, t), cover); err != nil {
			return outs, fmt.Errorf("pista %d: %w", t.Number, err)
		}
		outs = append(outs, out)
	}
	// Apartar el original.
	orig := filepath.Join(dir, "_original")
	if err := os.MkdirAll(orig, 0o755); err == nil {
		if err := os.Rename(path, filepath.Join(orig, filepath.Base(path))); err != nil {
			a.libLog("aviso: no pude mover el original: %v", err)
		} else {
			a.libLog("Original movido a _original\\")
		}
	}
	a.libLog("Separadas %d pistas", len(outs))
	return outs, nil
}
