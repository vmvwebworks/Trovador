package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Radio de recomendados: para cuando no apetece rebuscar. Va poniendo
// canciones de los artistas de «para ti» (lastfm.go, taste.go), cada una de
// la mejor fuente que la tenga entera:
//
//   - Bandcamp: una pista de uno de sus discos (entera, y el disco se sabe)
//   - si no, una de sus canciones más escuchadas en Deezer, entera desde
//     YouTube (yt-dlp la baja a la carpeta temporal de las escuchas)
//   - si YouTube falla, el fragmento de 30 s de Deezer
//   - si no está en Deezer, una de sus más escuchadas según Last.fm, desde
//     YouTube (sin disco conocido: para bajarlo se va a su discografía)
//
// Lo que gusta (♥) se guarda en likes.json con su disco, para bajarlo
// después, y cuenta para el gusto; lo que no (✕) descarta al artista.

// radioTrack es una canción de la radio.
type radioTrack struct {
	Artist   string   `json:"artist"`
	Title    string   `json:"title"`
	Album    string   `json:"album"`
	AlbumKey string   `json:"albumKey"` // el disco, para descargarlo ("" si no se sabe)
	Cover    string   `json:"cover"`
	CoverAlt []string `json:"coverAlt"`         // si la portada no carga: miniatura de YouTube, foto del artista
	Stream   string   `json:"stream,omitempty"` // URL (Bandcamp, Deezer): se toca por /preview
	Local    string   `json:"local,omitempty"`  // fichero (YouTube): se toca por /media
	Source   string   `json:"source"`           // bandcamp | youtube | deezer
	Full     bool     `json:"full"`             // entera (o fragmento de 30 s)
	Length   float64  `json:"length"`
	Because  []string `json:"because"` // artistas tuyos a los que se parece
	Tags     []string `json:"tags"`
	DeezerID string   `json:"deezerId"`
}

// radioPoolEntry: los artistas de una radio («para ti» o la de un artista).
type radioPoolEntry struct {
	list []discArtist
	at   time.Time
}

var radio = struct {
	mu     sync.Mutex
	pools  map[string]radioPoolEntry // por semilla normalizada ("" = para ti)
	mode   string                    // la radio que suena
	played map[string]bool           // artistas que ya han sonado en esta tanda
}{pools: map[string]radioPoolEntry{}, played: map[string]bool{}}

const radioPoolTTL = 6 * time.Hour

// RadioNext prepara la siguiente canción. Sin semilla, de «para ti» (la
// primera vez se calcula: con la caché de Last.fm, segundos; sin ella, un
// minuto o dos). Con semilla, la radio de ese artista: él y sus parecidos.
func (a *App) RadioNext(seed string) (radioTrack, error) {
	ctx := a.ctx
	seed = strings.TrimSpace(seed)
	pool, err := a.radioPool(seed)
	if err != nil {
		return radioTrack{}, err
	}
	radio.mu.Lock()
	if m := normalizeName(seed); radio.mode != m {
		radio.mode, radio.played = m, map[string]bool{} // otra radio: tanda nueva
	}
	radio.mu.Unlock()
	var lastErr error
	for tries := 0; tries < 6; tries++ {
		ar, ok := radioPick(pool)
		if !ok {
			return radioTrack{}, fmt.Errorf("no hay artistas que poner")
		}
		t, err := radioTrackFor(ctx, a, ar)
		if err == nil {
			return t, nil
		}
		lastErr = err
		a.libLog("radio: %s: %v", ar.Name, err)
	}
	return radioTrack{}, fmt.Errorf("no encuentro nada que poner (%v)", lastErr)
}

// radioPool: los artistas de una radio, calculados una vez cada pocas horas.
// La de un artista son él mismo y sus parecidos (con el filtro de estilo de
// Descubrir); los que suenan por él dicen «como <él>».
func (a *App) radioPool(seed string) ([]discArtist, error) {
	k := normalizeName(seed)
	radio.mu.Lock()
	if e, ok := radio.pools[k]; ok && len(e.list) > 0 && time.Since(e.at) < radioPoolTTL {
		radio.mu.Unlock()
		return e.list, nil
	}
	radio.mu.Unlock()
	var list []discArtist
	if seed == "" {
		fy, err := a.ForYou()
		if err != nil {
			return nil, err
		}
		list = fy.Artists
	} else {
		sim, err := a.SimilarArtists(seed, "", "")
		if err != nil {
			return nil, err
		}
		self := discArtist{Name: seed, Match: 1}
		if ar, ok := deezerFindArtist(a.ctx, seed); ok {
			self.DeezerID, self.Picture = ar.DeezerID, ar.Picture
		}
		list = append(list, self)
		for _, ar := range sim.Artists {
			if len(ar.Because) == 0 {
				ar.Because = []string{seed}
			}
			list = append(list, ar)
		}
		if len(list) == 1 {
			return nil, fmt.Errorf("no encuentro artistas parecidos a «%s»", seed)
		}
	}
	radio.mu.Lock()
	radio.pools[k] = radioPoolEntry{list: list, at: time.Now()}
	radio.mu.Unlock()
	return list, nil
}

// radioPick elige al azar un artista que no haya sonado en esta tanda ni
// esté descartado; los que más se parecen a tu gusto salen más.
func radioPick(pool []discArtist) (discArtist, bool) {
	radio.mu.Lock()
	defer radio.mu.Unlock()
	var cands []discArtist
	var total float64
	for round := 0; round < 2 && len(cands) == 0; round++ {
		if round == 1 {
			radio.played = map[string]bool{} // ya han sonado todos: otra tanda
		}
		for _, ar := range pool {
			if radio.played[normalizeName(ar.Name)] || isDismissed(ar.Name) {
				continue
			}
			cands = append(cands, ar)
			total += 0.2 + ar.Match
		}
	}
	if len(cands) == 0 {
		return discArtist{}, false
	}
	r := rand.Float64() * total
	pick := cands[len(cands)-1]
	for _, ar := range cands {
		if r -= 0.2 + ar.Match; r <= 0 {
			pick = ar
			break
		}
	}
	radio.played[normalizeName(pick.Name)] = true
	return pick, true
}

// radioTrackFor busca una canción del artista: Bandcamp, Deezer + YouTube o
// el fragmento de Deezer.
func radioTrackFor(ctx context.Context, a *App, ar discArtist) (radioTrack, error) {
	base := radioTrack{Artist: ar.Name, Because: ar.Because, Tags: ar.Tags, DeezerID: ar.DeezerID}
	if ar.Picture != "" {
		base.CoverAlt = []string{ar.Picture} // la foto del artista, por si no hay carátula
	}

	// 1. Bandcamp: uno de sus discos, una pista entera al azar.
	if rels, err := searchBandcamp(ctx, ar.Name, ""); err == nil {
		rand.Shuffle(len(rels), func(i, j int) { rels[i], rels[j] = rels[j], rels[i] })
		for i, r := range rels {
			if i >= 4 {
				break
			}
			if normalizeName(r.Artist) != normalizeName(ar.Name) {
				continue
			}
			d, err := getDetail(ctx, r.ID, "")
			if err != nil {
				continue
			}
			var ok []mbTrack
			for _, t := range d.Tracks {
				if t.Stream != "" && (t.Length == 0 || t.Length > 60) {
					ok = append(ok, t)
				}
			}
			if len(ok) == 0 {
				continue
			}
			t := ok[rand.Intn(len(ok))]
			out := base
			out.Title, out.Album, out.AlbumKey = t.Title, d.Release.Title, d.Release.ID
			out.Cover = firstNonEmpty(d.Release.CoverURL, r.CoverURL)
			out.Stream, out.Source, out.Full, out.Length = t.Stream, srcBandcamp, true, t.Length
			return out, nil
		}
	}

	// 2. Deezer: una de sus más escuchadas, entera desde YouTube.
	id := ar.DeezerID
	if id == "" {
		if found, ok := deezerFindArtist(ctx, ar.Name); ok {
			id = found.DeezerID
		}
	}
	if id != "" {
		top, err := deezerTopAlbums(ctx, id)
		if err == nil && len(top) > 0 {
			t := top[rand.Intn(min(5, len(top)))]
			out := base
			out.Title, out.Album, out.AlbumKey, out.Cover, out.Length = t.title, t.album, sourceKey(srcDeezer, t.albumID), t.cover, t.length
			if p, thumb, err := a.youtubeAudio(ar.Name, t.title); err == nil {
				out.Local, out.Source, out.Full = p, "youtube", true
				out.CoverAlt = append(out.CoverAlt, thumb)
				return out, nil
			}
			if t.preview != "" {
				out.Stream, out.Source, out.Length = t.preview, srcDeezer, 30
				return out, nil
			}
		}
	}

	// 3. Ni lo uno ni lo otro (grupos muy pequeños): sus canciones más
	// escuchadas según Last.fm, desde YouTube. El disco no se sabe.
	a.mu.Lock()
	key := strings.TrimSpace(a.settings.LastfmKey)
	a.mu.Unlock()
	if key != "" {
		if top, err := lastfmTopTracks(ctx, key, ar.Name); err == nil && len(top) > 0 {
			t := top[rand.Intn(min(5, len(top)))]
			if p, thumb, err := a.youtubeAudio(ar.Name, t.Title); err == nil {
				out := base
				out.Title, out.Local, out.Source, out.Full = t.Title, p, "youtube", true
				// Sin disco no hay carátula: la miniatura del vídeo.
				if thumb != "" {
					out.Cover = thumb
				}
				return out, nil
			}
		}
	}
	return radioTrack{}, fmt.Errorf("ni Bandcamp, ni Deezer, ni YouTube lo tienen")
}

type deezerTopTrack struct {
	title, artist, album, albumID, cover, preview string
	length                                        float64
}

// deezerTopAlbums: las más escuchadas del artista en Deezer, con su disco y
// su fragmento de 30 s.
func deezerTopAlbums(ctx context.Context, id string) ([]deezerTopTrack, error) {
	var raw struct {
		Data []struct {
			Title    string  `json:"title"`
			Duration float64 `json:"duration"`
			Preview  string  `json:"preview"`
			Artist   struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				ID      int64  `json:"id"`
				Title   string `json:"title"`
				CoverXL string `json:"cover_xl"`
			} `json:"album"`
		} `json:"data"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/artist/"+url.PathEscape(id)+"/top?limit=10", nil, nil, &raw); err != nil {
		return nil, err
	}
	var out []deezerTopTrack
	for _, t := range raw.Data {
		out = append(out, deezerTopTrack{
			title: t.Title, artist: t.Artist.Name, album: t.Album.Title, albumID: strconv.FormatInt(t.Album.ID, 10),
			cover: t.Album.CoverXL, preview: t.Preview, length: t.Duration,
		})
	}
	return out, nil
}

// ---- me gusta ----------------------------------------------------------------

// radioLike es una canción marcada con ♥.
type radioLike struct {
	Artist   string    `json:"artist"`
	Title    string    `json:"title"`
	Album    string    `json:"album"`
	AlbumKey string    `json:"albumKey"`
	Cover    string    `json:"cover"`
	Source   string    `json:"source"`
	At       time.Time `json:"at"`
}

var likes = struct {
	mu     sync.Mutex
	loaded bool
	list   []radioLike
}{}

func likesPath() string { return filepath.Join(cacheDir(), "likes.json") }

func loadLikes() {
	if likes.loaded {
		return
	}
	likes.loaded = true
	if data, err := os.ReadFile(likesPath()); err == nil {
		_ = json.Unmarshal(data, &likes.list)
	}
}

func saveLikes() error {
	data, err := json.MarshalIndent(likes.list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(likesPath(), data, 0o644)
}

// RadioLikes: lo marcado con ♥, lo último primero.
func (a *App) RadioLikes() []radioLike {
	likes.mu.Lock()
	defer likes.mu.Unlock()
	loadLikes()
	out := make([]radioLike, len(likes.list))
	for i, l := range likes.list {
		out[len(out)-1-i] = l
	}
	return out
}

// RadioLike marca una canción (si ya estaba, no se repite).
func (a *App) RadioLike(t radioTrack) ([]radioLike, error) {
	likes.mu.Lock()
	loadLikes()
	found := false
	for _, l := range likes.list {
		if strings.EqualFold(l.Artist, t.Artist) && strings.EqualFold(l.Title, t.Title) {
			found = true
		}
	}
	var err error
	if !found {
		cover := firstNonEmpty(append([]string{t.Cover}, t.CoverAlt...)...)
		likes.list = append(likes.list, radioLike{Artist: t.Artist, Title: t.Title, Album: t.Album, AlbumKey: t.AlbumKey, Cover: cover, Source: t.Source, At: time.Now()})
		err = saveLikes()
	}
	likes.mu.Unlock()
	return a.RadioLikes(), err
}

// RadioUnlike quita una canción de la lista.
func (a *App) RadioUnlike(artist, title string) ([]radioLike, error) {
	likes.mu.Lock()
	loadLikes()
	kept := likes.list[:0]
	for _, l := range likes.list {
		if !(strings.EqualFold(l.Artist, artist) && strings.EqualFold(l.Title, title)) {
			kept = append(kept, l)
		}
	}
	likes.list = kept
	err := saveLikes()
	likes.mu.Unlock()
	return a.RadioLikes(), err
}

// likedArtists: artista -> canciones con ♥ (para el gusto, taste.go).
func likedArtists() map[string]int {
	likes.mu.Lock()
	defer likes.mu.Unlock()
	loadLikes()
	out := map[string]int{}
	for _, l := range likes.list {
		out[l.Artist]++
	}
	return out
}
