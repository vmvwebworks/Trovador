package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Last.fm para Descubrir: etiquetas de estilo, caché en disco y ritmo de
// peticiones.
//
// Los parecidos de Last.fm salen de lo que escucha la misma gente, y con
// grupos pequeños (pocos oyentes) cuela de todo. Las etiquetas que la gente
// pone a cada artista («dungeon synth», «black metal»…) son mucho más
// fiables: un sugerido que no comparte ninguna etiqueta de peso con el
// artista de partida (o con la colección, en «para ti») se descarta.
//
// Last.fm pide no pasar de 5 peticiones por segundo; lo que ya se ha
// preguntado se guarda un mes en lastfm-cache.json.

type lfTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"` // 0-100: lo que pesa en ese artista
}

type lfEntry struct {
	At      time.Time    `json:"at"`
	Similar []discArtist `json:"similar,omitempty"`
	Tags    []lfTag      `json:"tags,omitempty"`
}

const lfTTL = 30 * 24 * time.Hour

var lfCache = struct {
	mu     sync.Mutex
	loaded bool
	dirty  bool
	m      map[string]lfEntry
}{}

// lfCachePath: lastfm-cache.json en la carpeta de datos (lo cambian los tests).
var lfCachePath = func() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "lastfm-cache.json")
}

func lfGet(key string) (lfEntry, bool) {
	lfCache.mu.Lock()
	defer lfCache.mu.Unlock()
	if !lfCache.loaded {
		lfCache.loaded = true
		lfCache.m = map[string]lfEntry{}
		if data, err := os.ReadFile(lfCachePath()); err == nil {
			_ = json.Unmarshal(data, &lfCache.m)
		}
	}
	e, ok := lfCache.m[key]
	return e, ok && time.Since(e.At) < lfTTL
}

func lfPut(key string, e lfEntry) {
	lfGet("") // asegura la carga
	lfCache.mu.Lock()
	e.At = time.Now()
	lfCache.m[key] = e
	lfCache.dirty = true
	lfCache.mu.Unlock()
}

// lfSave escribe la caché si ha cambiado (sin lo caducado).
func lfSave() {
	lfCache.mu.Lock()
	defer lfCache.mu.Unlock()
	p := lfCachePath()
	if !lfCache.dirty || p == "" {
		return
	}
	for k, e := range lfCache.m {
		if time.Since(e.At) >= lfTTL {
			delete(lfCache.m, k)
		}
	}
	if data, err := json.Marshal(lfCache.m); err == nil {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if os.WriteFile(p, data, 0o644) == nil {
			lfCache.dirty = false
		}
	}
}

// lfWait espacia las peticiones a Last.fm (5 por segundo como mucho).
var lfThrottle = struct {
	mu   sync.Mutex
	last time.Time
	gap  time.Duration
}{gap: 210 * time.Millisecond}

func lfWait(ctx context.Context) error {
	lfThrottle.mu.Lock()
	wait := lfThrottle.gap - time.Since(lfThrottle.last)
	if wait < 0 {
		wait = 0
	}
	lfThrottle.last = time.Now().Add(wait)
	lfThrottle.mu.Unlock()
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// similarCached: los parecidos de Last.fm, de la caché si están.
func similarCached(ctx context.Context, key, name, mbid string) ([]discArtist, error) {
	ck := "sim:" + normalizeName(name)
	if e, ok := lfGet(ck); ok {
		return e.Similar, nil
	}
	list, err := lastfmSimilar(ctx, key, name, mbid)
	if err != nil {
		return nil, err
	}
	lfPut(ck, lfEntry{Similar: list})
	return list, nil
}

// tagsCached: las etiquetas de un artista, de la caché si están.
func tagsCached(ctx context.Context, key, name string) ([]lfTag, error) {
	ck := "tags:" + normalizeName(name)
	if e, ok := lfGet(ck); ok {
		return e.Tags, nil
	}
	tags, err := lastfmTags(ctx, key, name)
	if err != nil {
		return nil, err
	}
	lfPut(ck, lfEntry{Tags: tags})
	return tags, nil
}

// lastfmTags: las etiquetas más puestas al artista (las de peso ≥ 5, hasta 12).
func lastfmTags(ctx context.Context, key, name string) ([]lfTag, error) {
	if err := lfWait(ctx); err != nil {
		return nil, err
	}
	params := url.Values{"method": {"artist.gettoptags"}, "api_key": {key}, "format": {"json"}, "autocorrect": {"1"}, "artist": {name}}
	var raw struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
		Top     struct {
			Tag []lfTag `json:"tag"`
		} `json:"toptags"`
	}
	if err := getJSON(ctx, http.MethodGet, lastfmBase+"?"+params.Encode(), nil, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Error == 6 { // no conoce al artista: sin etiquetas, no es un fallo
		return []lfTag{}, nil
	}
	if raw.Error != 0 {
		return nil, fmt.Errorf("%s", raw.Message)
	}
	out := []lfTag{}
	for _, t := range raw.Top.Tag {
		n := strings.ToLower(strings.TrimSpace(t.Name))
		if t.Count < 5 || n == "" || tagWeight(n) == 0 {
			continue
		}
		out = append(out, lfTag{Name: n, Count: t.Count})
		if len(out) == 12 {
			break
		}
	}
	return out, nil
}

// Etiquetas que no dicen nada del estilo.
var junkTags = map[string]bool{
	"seen live": true, "favorites": true, "favourites": true, "favorite": true, "favourite": true, "my favorites": true,
	"albums i own": true, "under 2000 listeners": true, "awesome": true, "love": true, "beautiful": true, "spotify": true,
	"check out": true, "good": true, "cool": true, "best": true, "amazing": true, "music": true, "all": true,
	"seen in concert": true, "to listen": true, "favorite artists": true, "my music": true, "great": true,
}

// Las de país o década dicen algo, pero poco: «norwegian» lo comparten el
// black metal y el pop noruego.
var weakTags = map[string]bool{
	"norwegian": true, "swedish": true, "finnish": true, "german": true, "american": true, "usa": true, "french": true,
	"british": true, "uk": true, "polish": true, "russian": true, "spanish": true, "italian": true, "canadian": true,
	"australian": true, "japanese": true, "dutch": true, "belgian": true, "greek": true, "brazilian": true, "mexican": true,
	"chilean": true, "ukrainian": true, "czech": true, "austrian": true, "swiss": true, "danish": true, "icelandic": true,
	"hungarian": true, "portuguese": true, "argentinian": true, "irish": true, "scottish": true, "english": true,
	"60s": true, "70s": true, "80s": true, "90s": true, "00s": true, "2000s": true, "2010s": true, "2020s": true,
	"male vocalists": true, "female vocalists": true, "instrumental": true,
}

func tagWeight(name string) float64 {
	switch {
	case junkTags[name]:
		return 0
	case weakTags[name]:
		return 0.3
	}
	return 1
}

// tagVector: el perfil de estilo de un artista (o la suma de varios).
func tagVector(tags []lfTag, scale float64, into map[string]float64) map[string]float64 {
	if into == nil {
		into = map[string]float64{}
	}
	for _, t := range tags {
		into[t.Name] += scale * float64(t.Count) / 100 * tagWeight(t.Name)
	}
	return into
}

// cosine: lo que se parecen dos perfiles de estilo (0 nada, 1 idénticos).
func cosine(a, b map[string]float64) float64 {
	var dot, na, nb float64
	for k, v := range a {
		na += v * v
		dot += v * b[k]
	}
	for _, v := range b {
		nb += v * v
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// styleFilter compara cada sugerido con su perfil de estilo (el del artista
// de partida, o en «para ti» el de los artistas tuyos que lo recomiendan):
// descarta los que no comparten nada (cosine < 0,05) y reordena el resto por
// parecido × estilo. Los que Last.fm no tiene etiquetados se quedan, algo
// más abajo, igual que si no hay perfil con el que comparar.
func styleFilter(ctx context.Context, key string, profileOf func(i int) map[string]float64, list []discArtist) (kept []discArtist, dropped int) {
	type res struct {
		tags []lfTag
		ok   bool
	}
	out := make([]res, len(list))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tags, err := tagsCached(ctx, key, list[i].Name)
			out[i] = res{tags, err == nil}
		}(i)
	}
	wg.Wait()
	for i, ar := range list {
		r := out[i]
		for _, t := range r.tags {
			if tagWeight(t.Name) == 1 && len(ar.Tags) < 3 {
				ar.Tags = append(ar.Tags, t.Name)
			}
		}
		profile := profileOf(i)
		if !r.ok || len(r.tags) == 0 || len(profile) == 0 {
			ar.Match *= 0.7 // sin datos de estilo: ni se descarta ni se fía uno
			kept = append(kept, ar)
			continue
		}
		c := cosine(profile, tagVector(r.tags, 1, nil))
		if c < 0.05 {
			dropped++
			continue
		}
		ar.Style = c
		ar.Match *= 0.35 + 0.65*math.Min(1, c/0.4)
		kept = append(kept, ar)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Match > kept[j].Match })
	return kept, dropped
}

// ---- para ti ---------------------------------------------------------------

var artistSplitRe = regexp.MustCompile(`\s*(?:/|,|;|&| x | vs\.? | with | feat\.? | ft\.? )\s*`)

// splitArtists separa un split o una colaboración («Krayl / Erroiak»,
// «Saltatio Mortis, Versengold, …») en sus artistas, para cuando Last.fm no
// conoce el nombre compuesto (con «Simon & Garfunkel» sí, por eso no se
// separa de entrada). Un nombre sin separadores vuelve tal cual.
func splitArtists(s string) []string {
	var out []string
	for _, p := range artistSplitRe.Split(strings.TrimSpace(s), -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// forYouResult: recomendaciones a partir del gusto del usuario.
type forYouResult struct {
	Artists  []discArtist `json:"artists"`
	Seeds    int          `json:"seeds"`   // artistas tuyos consultados
	Basis    []tasteSeed  `json:"basis"`   // los que más pesan, para enseñar de dónde sale
	Dropped  int          `json:"dropped"` // descartados por estilo
	Warnings []string     `json:"warnings"`
}

// forYouProgress va a la ventana con el evento "foryou".
type forYouProgress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

const forYouSeeds = 100 // artistas tuyos que se consultan como mucho

// ForYou recomienda artistas a partir del gusto (taste.go): los parecidos
// de los artistas que más te gustan se suman, cada uno con lo que pesa ese
// artista para ti; se quita lo que ya tienes y lo descartado; se filtra por
// el estilo de quien los recomienda, y lo muy conocido baja (si tiene
// medio millón de oyentes en Last.fm, ya lo conoces).
func (a *App) ForYou() (forYouResult, error) {
	ctx := a.ctx
	a.mu.Lock()
	key := strings.TrimSpace(a.settings.LastfmKey)
	a.mu.Unlock()
	if key == "" {
		return forYouResult{}, fmt.Errorf("hace falta la API key de Last.fm (Ajustes)")
	}
	defer lfSave()
	progress := func(phase string, done, total int) {
		a.emit("foryou", forYouProgress{Phase: phase, Done: done, Total: total})
	}

	progress("tu gusto", 0, 0)
	seeds := a.tasteSeeds(ctx, key)
	if len(seeds) == 0 {
		return forYouResult{}, fmt.Errorf("no encuentro artistas en la colección")
	}
	if len(seeds) > forYouSeeds {
		seeds = seeds[:forYouSeeds]
	}
	out := forYouResult{Seeds: len(seeds), Basis: seeds[:min(12, len(seeds))]}
	weight := map[string]float64{}
	for _, s := range seeds {
		weight[s.Name] = s.Weight
	}
	owned := a.ownedAlbums()
	ownedKey := map[string]bool{}
	for _, o := range owned {
		ownedKey[normalizeName(o.artist)] = true
	}

	// 1. Parecidos de cada uno, sumados con su peso.
	type cand struct {
		ar      discArtist
		score   float64
		because []string
	}
	var (
		mu    sync.Mutex
		cands = map[string]*cand{}
		done  int
		fails int
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 4)
	)
	for _, s := range seeds {
		wg.Add(1)
		go func(s tasteSeed) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sim, err := similarCached(ctx, key, s.Name, "")
			// Un split o una colaboración que Last.fm no conoce entero: los
			// parecidos de cada uno de sus artistas.
			if parts := splitArtists(s.Name); err == nil && len(sim) == 0 && len(parts) > 1 {
				for _, p := range parts {
					more, _ := similarCached(ctx, key, p, "")
					sim = append(sim, more...)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			done++
			progress("parecidos", done, len(seeds))
			if err != nil {
				fails++
				return
			}
			for _, ar := range sim {
				k := normalizeName(ar.Name)
				if ownedKey[k] {
					continue // ya lo tienes
				}
				c := cands[k]
				if c == nil {
					c = &cand{ar: ar}
					cands[k] = c
				}
				c.score += ar.Match * s.Weight
				c.because = append(c.because, s.Name)
			}
		}(s)
	}
	wg.Wait()
	if fails > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("last.fm no ha contestado por %d artista(s)", fails))
	}
	list := make([]*cand, 0, len(cands))
	for _, c := range cands {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })

	// 2. Fuera lo descartado y lo que ya está en la colección con otro
	// nombre (ownedBy es más tolerante que la clave normalizada). Los
	// «porque» se ordenan por lo que pesa cada artista tuyo.
	arts := make([]discArtist, 0, 120)
	for _, c := range list {
		if len(arts) == 120 {
			break
		}
		if isDismissed(c.ar.Name) || len(ownedBy(owned, c.ar.Name)) > 0 {
			continue
		}
		ar := c.ar
		ar.Match = c.score
		ar.Because = dedupe(c.because)
		sort.SliceStable(ar.Because, func(i, j int) bool { return weight[ar.Because[i]] > weight[ar.Because[j]] })
		ar.Sources = []string{"lastfm"}
		arts = append(arts, ar)
	}

	// 3. El estilo: cada sugerido se compara con el de los artistas tuyos
	// que lo recomiendan (los 5 que más pesan), no con el de toda la
	// colección, que si es variada lo deja pasar todo.
	need := map[string]bool{}
	for _, ar := range arts {
		for i, b := range ar.Because {
			if i < 5 {
				need[b] = true
			}
		}
	}
	seedTags := map[string][]lfTag{}
	n := 0
	for b := range need {
		progress("estilo de tus artistas", n, len(need))
		n++
		if tags, err := tagsCached(ctx, key, b); err == nil {
			seedTags[b] = tags
		}
	}
	profileOf := func(i int) map[string]float64 {
		p := map[string]float64{}
		for j, b := range arts[i].Because {
			if j < 5 {
				tagVector(seedTags[b], weight[b], p)
			}
		}
		return p
	}
	progress("filtrando por estilo", 0, len(arts))
	arts, out.Dropped = styleFilter(ctx, key, profileOf, arts)

	// 4. Lo conocido que es cada uno: lo muy escuchado baja.
	listeners := make([]int, len(arts))
	var wg2 sync.WaitGroup
	done = 0
	for i := range arts {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n, err := listenersCached(ctx, key, arts[i].Name)
			if err != nil {
				n = -1
			}
			listeners[i] = n
			mu.Lock()
			done++
			progress("lo conocido que es cada uno", done, len(arts))
			mu.Unlock()
		}(i)
	}
	wg2.Wait()
	for i := range arts {
		arts[i].Listeners = listeners[i]
		arts[i].Match *= obscurity(listeners[i])
	}
	sort.SliceStable(arts, func(i, j int) bool { return arts[i].Match > arts[j].Match })
	if len(arts) > 60 {
		arts = arts[:60]
	}
	if len(arts) > 0 {
		top := arts[0].Match
		for i := range arts {
			arts[i].Match /= top
		}
	}
	progress("fotos", 0, len(arts))
	fillPictures(ctx, arts)
	out.Artists = arts
	return out, nil
}
