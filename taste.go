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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// El gusto del usuario, para «para ti»: no basta con lo que hay en la
// colección (tener muchos discos de alguien no quiere decir escucharlo).
// Pesan, de más a menos:
//
//   - los favoritos ♥ del reproductor y las canciones ♥ de la radio
//   - lo que más se escucha: las escuchas del reproductor que cuentan
//     (la mitad de la pista, como el scrobbling; se cuentan aunque no haya
//     Last.fm conectado) y, si lo hay, los artistas más escuchados en Last.fm
//   - lo añadido hace poco
//   - y, poco, cuántos discos suyos hay
//
// Y lo que el usuario descarta («ya lo conozco / no me interesa») no vuelve
// a salir.

// ---- escuchas del reproductor -----------------------------------------------

var plays = struct {
	mu     sync.Mutex
	loaded bool
	n      map[string]int // artista normalizado -> escuchas
	names  map[string]string
}{}

func playsPath() string { return filepath.Join(cacheDir(), "plays.json") }

func loadPlays() {
	if plays.loaded {
		return
	}
	plays.loaded = true
	plays.n, plays.names = map[string]int{}, map[string]string{}
	var disk struct {
		N     map[string]int    `json:"n"`
		Names map[string]string `json:"names"`
	}
	if data, err := os.ReadFile(playsPath()); err == nil && json.Unmarshal(data, &disk) == nil && disk.N != nil {
		plays.n, plays.names = disk.N, disk.Names
		if plays.names == nil {
			plays.names = map[string]string{}
		}
	}
}

// countPlay apunta una escucha de un artista.
func countPlay(artist string) {
	k := normalizeName(artist)
	if k == "" {
		return
	}
	plays.mu.Lock()
	defer plays.mu.Unlock()
	loadPlays()
	plays.n[k]++
	plays.names[k] = artist
	data, _ := json.Marshal(map[string]any{"n": plays.n, "names": plays.names})
	_ = os.MkdirAll(cacheDir(), 0o755)
	_ = os.WriteFile(playsPath(), data, 0o644)
}

// playCounts: artista (tal como se escribió) -> escuchas.
func playCounts() map[string]int {
	plays.mu.Lock()
	defer plays.mu.Unlock()
	loadPlays()
	out := map[string]int{}
	for k, n := range plays.n {
		out[firstNonEmpty(plays.names[k], k)] = n
	}
	return out
}

// ---- descartados --------------------------------------------------------------

var dismissed = struct {
	mu     sync.Mutex
	loaded bool
	set    map[string]bool
}{}

func dismissedPath() string { return filepath.Join(cacheDir(), "dismissed.json") }

func isDismissed(name string) bool {
	dismissed.mu.Lock()
	defer dismissed.mu.Unlock()
	loadDismissed()
	return dismissed.set[normalizeName(name)]
}

func loadDismissed() {
	if dismissed.loaded {
		return
	}
	dismissed.loaded = true
	dismissed.set = map[string]bool{}
	var list []string
	if data, err := os.ReadFile(dismissedPath()); err == nil && json.Unmarshal(data, &list) == nil {
		for _, k := range list {
			dismissed.set[k] = true
		}
	}
}

// DismissArtist: «ya lo conozco / no me interesa»; no vuelve a sugerirse.
// Con undo, se deshace.
func (a *App) DismissArtist(name string, undo bool) error {
	k := normalizeName(name)
	if k == "" {
		return fmt.Errorf("falta el artista")
	}
	dismissed.mu.Lock()
	defer dismissed.mu.Unlock()
	loadDismissed()
	if undo {
		delete(dismissed.set, k)
	} else {
		dismissed.set[k] = true
	}
	list := make([]string, 0, len(dismissed.set))
	for k := range dismissed.set {
		list = append(list, k)
	}
	sort.Strings(list)
	data, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dismissedPath(), data, 0o644)
}

// ---- semillas -------------------------------------------------------------------

// tasteSeed es un artista del que partir, con lo que pesa y por qué.
type tasteSeed struct {
	Name   string   `json:"name"`
	Weight float64  `json:"weight"`
	Why    []string `json:"why"` // favorito | escuchas | last.fm | reciente | colección
	discs  int
}

// tasteSeeds reúne las señales de gusto y devuelve los artistas de los que
// partir, el que más pesa primero.
func (a *App) tasteSeeds(ctx context.Context, lastfmKey string) []tasteSeed {
	by := map[string]*tasteSeed{}
	get := func(name string) *tasteSeed {
		k := normalizeName(name)
		if k == "" || k == "various artists" || k == "varios" || k == "va" {
			return nil
		}
		s := by[k]
		if s == nil {
			s = &tasteSeed{Name: name}
			by[k] = s
		}
		return s
	}
	addWhy := func(s *tasteSeed, why string) {
		for _, w := range s.Why {
			if w == why {
				return
			}
		}
		s.Why = append(s.Why, why)
	}

	// La colección: cuántos discos y cuáles son recientes. La fecha de la
	// carpeta cambia también al renombrar, etiquetar u ordenar: si «reciente»
	// sale para más de un tercio de la colección, no dice nada y no cuenta.
	owned := a.ownedAlbums()
	artistOf := map[string]string{}
	var recent []string
	for _, o := range owned {
		artistOf[strings.ToLower(filepath.Clean(o.dir))] = o.artist
		s := get(o.artist)
		if s == nil {
			continue
		}
		s.discs++
		if info, err := os.Stat(o.dir); err == nil && time.Since(info.ModTime()) < 45*24*time.Hour {
			recent = append(recent, o.artist)
		}
	}
	if len(recent) <= len(owned)/3 {
		n := map[string]int{}
		for _, name := range recent {
			if s := get(name); s != nil && n[name] < 2 { // hasta dos discos recientes cuentan
				n[name]++
				s.Weight += 1
				addWhy(s, "reciente")
			}
		}
	}
	for _, s := range by {
		s.Weight += 0.3 * math.Log(1+float64(s.discs))
	}

	// Favoritos ♥ (de un mismo artista cuentan hasta dos discos: con más,
	// uno solo arrastraría todas las sugerencias).
	if dirs, err := a.Favorites(); err == nil {
		n := map[*tasteSeed]int{}
		for _, d := range dirs {
			name := artistOf[strings.ToLower(filepath.Clean(d))]
			if name == "" {
				name, _ = folderNames(d)
			}
			if s := get(name); s != nil && n[s] < 2 {
				n[s]++
				s.Weight += 2.5
				addWhy(s, "favorito")
			}
		}
	}

	// Lo marcado con ♥ en la radio (hasta dos canciones por artista).
	for name, n := range likedArtists() {
		if s := get(name); s != nil {
			s.Weight += 2 * float64(min(n, 2))
			addWhy(s, "me gusta")
		}
	}

	// Lo que se escucha en el reproductor.
	for name, n := range playCounts() {
		if s := get(name); s != nil {
			s.Weight += 1.5 * math.Log2(1+float64(n))
			addWhy(s, "escuchas")
		}
	}

	// Lo más escuchado en Last.fm (últimos 6 meses), si hay cuenta conectada.
	a.mu.Lock()
	user := a.settings.LastfmUser
	a.mu.Unlock()
	if user != "" && lastfmKey != "" {
		if top, err := lastfmUserTop(ctx, lastfmKey, user); err == nil {
			for i, name := range top {
				if s := get(name); s != nil {
					s.Weight += 3 * (1 - float64(i)/float64(len(top)))
					addWhy(s, "last.fm")
				}
			}
		}
	}

	out := make([]tasteSeed, 0, len(by))
	for _, s := range by {
		if len(s.Why) == 0 {
			s.Why = []string{"colección"}
		}
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// lastfmUserTop: los artistas más escuchados del usuario en Last.fm.
func lastfmUserTop(ctx context.Context, key, user string) ([]string, error) {
	if e, ok := lfGet("usertop:" + strings.ToLower(user)); ok && time.Since(e.At) < 24*time.Hour {
		names := make([]string, len(e.Similar))
		for i, s := range e.Similar {
			names[i] = s.Name
		}
		return names, nil
	}
	if err := lfWait(ctx); err != nil {
		return nil, err
	}
	params := url.Values{"method": {"user.gettopartists"}, "api_key": {key}, "format": {"json"}, "user": {user}, "period": {"6month"}, "limit": {"50"}}
	var raw struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
		Top     struct {
			Artist []struct {
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"topartists"`
	}
	if err := getJSON(ctx, http.MethodGet, lastfmBase+"?"+params.Encode(), nil, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Error != 0 {
		return nil, fmt.Errorf("%s", raw.Message)
	}
	var names []string
	var cached []discArtist
	for _, ar := range raw.Top.Artist {
		names = append(names, ar.Name)
		cached = append(cached, discArtist{Name: ar.Name})
	}
	lfPut("usertop:"+strings.ToLower(user), lfEntry{Similar: cached})
	return names, nil
}

// ---- lo conocido que es cada uno ----------------------------------------------

// listenersCached: cuántos oyentes tiene el artista en Last.fm (-1 si no lo
// conoce).
func listenersCached(ctx context.Context, key, name string) (int, error) {
	ck := "info:" + normalizeName(name)
	if e, ok := lfGet(ck); ok && len(e.Similar) == 1 {
		return e.Similar[0].Fans, nil
	}
	if err := lfWait(ctx); err != nil {
		return 0, err
	}
	params := url.Values{"method": {"artist.getinfo"}, "api_key": {key}, "format": {"json"}, "autocorrect": {"1"}, "artist": {name}}
	var raw struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
		Artist  struct {
			Stats struct {
				Listeners string `json:"listeners"`
			} `json:"stats"`
		} `json:"artist"`
	}
	if err := getJSON(ctx, http.MethodGet, lastfmBase+"?"+params.Encode(), nil, nil, &raw); err != nil {
		return 0, err
	}
	n := -1
	if raw.Error == 0 {
		n, _ = strconv.Atoi(raw.Artist.Stats.Listeners)
	} else if raw.Error != 6 {
		return 0, fmt.Errorf("%s", raw.Message)
	}
	// Se guarda en el mismo formato de la caché (un discArtist con Fans).
	lfPut(ck, lfEntry{Similar: []discArtist{{Name: name, Fans: n}}})
	return n, nil
}

// obscurity: cuánto se premia que un artista sea poco conocido. Con 1.000
// oyentes, 0,85; con 5.000, 0,7; con 20.000, 0,5; con 85.000, 0,3; con medio
// millón, 0,15.
func obscurity(listeners int) float64 {
	if listeners < 0 {
		return 0.8 // Last.fm no lo conoce: pequeño, pero sin datos
	}
	return 1 / (1 + math.Pow(float64(listeners)/20000, 0.6))
}
