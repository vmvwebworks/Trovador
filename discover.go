package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Descubrir: buscar fuera de la biblioteca sin tener que abrir un disco,
// ver la discografía de un artista con lo que ya se tiene marcado, y saltar
// a artistas parecidos. Todo lo que sale se coteja con la colección para
// distinguir «ya lo tienes» de lo que falta.
//
//   - discografía: Deezer (/artist/{id}/albums, con carátulas) y MusicBrainz
//     (ediciones oficiales del artista agrupadas por disco), fundidas por título
//   - parecidos: Deezer «related» siempre; Last.fm artist.getSimilar si hay
//     clave en Ajustes (gratuita: last.fm/api/account/create)

// discRelease es un disco de una discografía o de una búsqueda.
type discRelease struct {
	mbRelease
	Kind  string `json:"kind"`  // album | ep | single | compile | live
	Owned string `json:"owned"` // carpeta del disco si ya está en la colección
}

// discArtist es un artista encontrado o sugerido.
type discArtist struct {
	Name     string   `json:"name"`
	DeezerID string   `json:"deezerId"`
	MBID     string   `json:"mbid"`
	Picture  string   `json:"picture"`
	Fans     int      `json:"fans"`
	Match    float64  `json:"match"`   // parecido 0..1 (solo en parecidos)
	Sources  []string `json:"sources"` // de dónde sale la sugerencia
	Owned    int      `json:"owned"`   // discos suyos en la colección
}

type discoverResult struct {
	Artists  []discArtist  `json:"artists"`
	Releases []discRelease `json:"releases"`
	Warnings []string      `json:"warnings"`
}

type discography struct {
	Artist   discArtist    `json:"artist"`
	Releases []discRelease `json:"releases"`
	Warnings []string      `json:"warnings"`
}

type similarResult struct {
	Artists  []discArtist `json:"artists"`
	Warnings []string     `json:"warnings"`
	LastFM   bool         `json:"lastfm"` // se consultó Last.fm (hay clave)
}

// ---- Métodos expuestos a la ventana --------------------------------------

// DiscoverSearch busca artistas y discos a la vez. «Artista - Álbum» se
// separa; si no, el texto vale como nombre de artista y como título.
func (a *App) DiscoverSearch(query string) (discoverResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return discoverResult{}, fmt.Errorf("escribe un artista o un disco")
	}
	artist, album := "", q
	if l, r, ok := strings.Cut(q, " - "); ok {
		artist, album = strings.TrimSpace(l), strings.TrimSpace(r)
	}
	var (
		res     discoverResult
		rels    []mbRelease
		artErr  error
		wg      sync.WaitGroup
		artists []discArtist
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		artists, artErr = deezerArtists(a.ctx, firstNonEmpty(artist, q), 8)
	}()
	go func() {
		defer wg.Done()
		rels, res.Warnings = searchAll(a.ctx, artist, album, a.searchOpts(false))
	}()
	wg.Wait()
	if artErr != nil {
		res.Warnings = append(res.Warnings, "deezer (artistas): "+artErr.Error())
	}

	owned := a.ownedAlbums()
	for _, ar := range artists {
		ar.Owned = len(ownedBy(owned, ar.Name))
		res.Artists = append(res.Artists, ar)
	}
	for _, r := range rels {
		res.Releases = append(res.Releases, discRelease{mbRelease: r, Kind: deezerKind(r.Format), Owned: ownedDir(owned, r.Artist, r.Title)})
	}
	return res, nil
}

// ArtistDiscography junta la discografía de Deezer y la de MusicBrainz. El
// artista se identifica por su id de Deezer y su MBID si se conocen; si no,
// por el nombre (y, si tenemos discos suyos, a través de ellos: «Frost» hay
// veinte).
func (a *App) ArtistDiscography(name, deezerID, mbid string) (discography, error) {
	ctx := a.ctx
	name = strings.TrimSpace(name)
	if name == "" {
		return discography{}, fmt.Errorf("falta el artista")
	}
	owned := a.ownedAlbums()
	mine := ownedBy(owned, name)
	out := discography{Artist: discArtist{Name: name, DeezerID: deezerID, MBID: mbid, Owned: len(mine)}}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		dz  []discRelease
		mbz []discRelease
	)
	warn := func(s string) { mu.Lock(); out.Warnings = append(out.Warnings, s); mu.Unlock() }
	wg.Add(2)
	go func() {
		defer wg.Done()
		id := deezerID
		if id == "" {
			ar, ok := deezerFindArtist(ctx, name)
			if !ok {
				return
			}
			id = ar.DeezerID
			mu.Lock()
			out.Artist.DeezerID, out.Artist.Picture, out.Artist.Fans = ar.DeezerID, ar.Picture, ar.Fans
			mu.Unlock()
		} else if ar, err := deezerArtist(ctx, id); err == nil {
			mu.Lock()
			out.Artist.Picture, out.Artist.Fans = ar.Picture, ar.Fans
			mu.Unlock()
		}
		rels, err := deezerArtistAlbums(ctx, id, name)
		if err != nil {
			warn("deezer: " + err.Error())
		}
		dz = rels
	}()
	go func() {
		defer wg.Done()
		id := mbid
		if id == "" && len(mine) > 0 {
			dirs := make([]string, len(mine))
			for i, o := range mine {
				dirs[i] = o.dir
			}
			id = a.artistMBID(ctx, name, dirs)
		}
		if id == "" {
			var w string
			id, w = mbFindArtist(ctx, name)
			if w != "" {
				warn(w)
			}
		}
		if id == "" {
			return
		}
		mu.Lock()
		out.Artist.MBID = id
		mu.Unlock()
		rels, err := mbArtistReleases(ctx, id)
		if err != nil {
			warn("musicbrainz: " + err.Error())
		}
		mbz = rels
	}()
	wg.Wait()

	out.Releases = mergeDiscographies(mbz, dz)
	for i := range out.Releases {
		r := &out.Releases[i]
		r.Owned = ownedDir(mine, name, r.Title)
	}
	if len(out.Releases) == 0 && len(out.Warnings) == 0 {
		out.Warnings = append(out.Warnings, "ni Deezer ni MusicBrainz tienen discos de «"+name+"»")
	}
	return out, nil
}

// SimilarArtists sugiere artistas parecidos: Deezer siempre y Last.fm si hay
// clave. Si salen en las dos, suben. Los que ya están en la colección se
// marcan (la ventana los manda al final).
func (a *App) SimilarArtists(name, deezerID, mbid string) (similarResult, error) {
	ctx := a.ctx
	name = strings.TrimSpace(name)
	a.mu.Lock()
	key := strings.TrimSpace(a.settings.LastfmKey)
	a.mu.Unlock()
	out := similarResult{LastFM: key != ""}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		dz, lf []discArtist
	)
	warn := func(s string) { mu.Lock(); out.Warnings = append(out.Warnings, s); mu.Unlock() }
	wg.Add(1)
	go func() {
		defer wg.Done()
		id := deezerID
		if id == "" {
			ar, ok := deezerFindArtist(ctx, name)
			if !ok {
				warn("deezer: no encuentro a «" + name + "»")
				return
			}
			id = ar.DeezerID
		}
		rels, err := deezerRelated(ctx, id)
		if err != nil {
			warn("deezer: " + err.Error())
		}
		dz = rels
	}()
	if key != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rels, err := lastfmSimilar(ctx, key, name, mbid)
			if err != nil {
				warn("last.fm: " + err.Error())
			}
			lf = rels
		}()
	}
	wg.Wait()

	out.Artists = mergeSimilar(name, lf, dz, 30)
	fillPictures(ctx, out.Artists)
	owned := a.ownedAlbums()
	for i := range out.Artists {
		out.Artists[i].Owned = len(ownedBy(owned, out.Artists[i].Name))
	}
	return out, nil
}

// ---- la colección: qué discos tenemos -----------------------------------

// ownedAlbum es un disco de la colección con el artista y título que se
// deducen sin abrir nada (caché de tarjetas o nombre de la carpeta).
type ownedAlbum struct {
	dir, artist, album string
}

var ownedCache struct {
	mu   sync.Mutex
	at   time.Time
	list []ownedAlbum
}

// ownedAlbums recorre la colección y la carpeta de descargas. Se guarda
// medio minuto: navegando de un artista a otro no hace falta releer.
func (a *App) ownedAlbums() []ownedAlbum {
	ownedCache.mu.Lock()
	defer ownedCache.mu.Unlock()
	if time.Since(ownedCache.at) < 30*time.Second {
		return ownedCache.list
	}
	a.mu.Lock()
	roots := []string{a.settings.OutDir}
	a.mu.Unlock()
	roots = append(roots, a.collectionRoot())
	seen := map[string]bool{}
	var list []ownedAlbum
	for _, root := range roots {
		if root == "" || !dirExists(root) {
			continue
		}
		dirs, err := albumDirs(a.ctx, root)
		if err != nil {
			continue
		}
		for _, dir := range dirs {
			if seen[dir] {
				continue
			}
			seen[dir] = true
			artist, album := folderNames(dir)
			list = append(list, ownedAlbum{dir: dir, artist: artist, album: album})
		}
	}
	ownedCache.list, ownedCache.at = list, time.Now()
	return list
}

// forgetOwned obliga a releer la colección (tras crear un disco).
func forgetOwned() {
	ownedCache.mu.Lock()
	ownedCache.at = time.Time{}
	ownedCache.mu.Unlock()
}

// folderNames: artista y álbum de un disco tal como los ve el reproductor:
// la tarjeta si ya se leyó, si no lo que dice el nombre de la carpeta (y el
// de la carpeta del artista que la contiene).
func folderNames(dir string) (artist, album string) {
	if card, ok := cachedCard(dir); ok {
		return card.Artist, card.Album
	}
	artist, album = guessNames(filepath.Base(dir), nil)
	if artist == "" {
		artist = filepath.Base(filepath.Dir(dir))
	}
	return artist, album
}

// ownedBy: los discos de la colección de ese artista.
func ownedBy(owned []ownedAlbum, artist string) []ownedAlbum {
	var out []ownedAlbum
	for _, o := range owned {
		if similarity(o.artist, artist) >= 0.85 {
			out = append(out, o)
		}
	}
	return out
}

// ownedDir: la carpeta del disco si está en la colección ("" si no).
func ownedDir(owned []ownedAlbum, artist, title string) string {
	t := stripParen(title)
	for _, o := range owned {
		if similarity(stripParen(o.album), t) >= 0.85 && (artist == "" || similarity(o.artist, artist) >= 0.85) {
			return o.dir
		}
	}
	return ""
}

// ---- Deezer ---------------------------------------------------------------

type deezerArtistJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture_medium"`
	Fans    int    `json:"nb_fan"`
}

func (d deezerArtistJSON) artist() discArtist {
	pic := d.Picture
	if strings.Contains(pic, "/artist//") { // sin foto: Deezer da una URL vacía por dentro
		pic = ""
	}
	return discArtist{Name: d.Name, DeezerID: strconv.FormatInt(d.ID, 10), Picture: pic, Fans: d.Fans, Sources: []string{srcDeezer}}
}

func deezerArtists(ctx context.Context, q string, limit int) ([]discArtist, error) {
	var raw struct {
		Data []deezerArtistJSON `json:"data"`
	}
	u := fmt.Sprintf("https://api.deezer.com/search/artist?limit=%d&q=%s", limit, url.QueryEscape(q))
	if err := getJSON(ctx, http.MethodGet, u, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]discArtist, 0, len(raw.Data))
	for _, d := range raw.Data {
		out = append(out, d.artist())
	}
	return out, nil
}

func deezerArtist(ctx context.Context, id string) (discArtist, error) {
	var raw deezerArtistJSON
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/artist/"+url.PathEscape(id), nil, nil, &raw); err != nil {
		return discArtist{}, err
	}
	if raw.ID == 0 {
		return discArtist{}, fmt.Errorf("no está en Deezer")
	}
	return raw.artist(), nil
}

// deezerFindArtist: el artista con ese nombre exacto (normalizado) y más
// seguidores; si no hay ninguno exacto, el primero que se parezca mucho.
func deezerFindArtist(ctx context.Context, name string) (discArtist, bool) {
	list, err := deezerArtists(ctx, name, 10)
	if err != nil {
		return discArtist{}, false
	}
	best, found := discArtist{}, false
	for _, ar := range list {
		if normalizeName(ar.Name) == normalizeName(name) && (!found || ar.Fans > best.Fans) {
			best, found = ar, true
		}
	}
	if found {
		return best, true
	}
	for _, ar := range list {
		if similarity(ar.Name, name) >= 0.9 {
			return ar, true
		}
	}
	return discArtist{}, false
}

func deezerArtistAlbums(ctx context.Context, id, artist string) ([]discRelease, error) {
	var raw struct {
		Data []struct {
			ID      int64  `json:"id"`
			Title   string `json:"title"`
			CoverXL string `json:"cover_xl"`
			Date    string `json:"release_date"`
			Type    string `json:"record_type"`
		} `json:"data"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/artist/"+url.PathEscape(id)+"/albums?limit=200", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]discRelease, 0, len(raw.Data))
	for _, d := range raw.Data {
		out = append(out, discRelease{
			mbRelease: mbRelease{
				ID: sourceKey(srcDeezer, strconv.FormatInt(d.ID, 10)), Source: srcDeezer,
				Title: d.Title, Artist: artist, Date: d.Date, Format: "Digital (" + d.Type + ")", CoverURL: d.CoverXL,
			},
			Kind: deezerKind(d.Type),
		})
	}
	return out, nil
}

// deezerKind traduce el record_type de Deezer (que también va dentro del
// formato, «Digital (ep)») a nuestras clases de disco.
func deezerKind(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "compile"):
		return "compile"
	case strings.Contains(s, "single"):
		return "single"
	case strings.Contains(s, "ep"):
		return "ep"
	}
	return "album"
}

func deezerRelated(ctx context.Context, id string) ([]discArtist, error) {
	var raw struct {
		Data []deezerArtistJSON `json:"data"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/artist/"+url.PathEscape(id)+"/related?limit=30", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]discArtist, 0, len(raw.Data))
	for i, d := range raw.Data {
		ar := d.artist()
		// Deezer no da puntuación: la posición en su lista hace de parecido.
		ar.Match = 0.9 - 0.5*float64(i)/float64(len(raw.Data))
		out = append(out, ar)
	}
	return out, nil
}

// fillPictures: a los sugeridos que solo vienen de Last.fm (que ya no da
// fotos) se les busca en Deezer la foto y el id, unos cuantos a la vez.
func fillPictures(ctx context.Context, list []discArtist) {
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i := range list {
		if list[i].DeezerID != "" {
			continue
		}
		wg.Add(1)
		go func(ar *discArtist) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if found, ok := deezerFindArtist(ctx, ar.Name); ok {
				ar.DeezerID, ar.Picture, ar.Fans = found.DeezerID, found.Picture, found.Fans
			}
		}(&list[i])
	}
	wg.Wait()
}

// ---- MusicBrainz ----------------------------------------------------------

// mbFindArtist busca el MBID por el nombre. Solo se fía si hay un único
// artista que se llame exactamente así; si hay varios, avisa.
func mbFindArtist(ctx context.Context, name string) (mbid, warning string) {
	var raw struct {
		Artists []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Score          int    `json:"score"`
			Disambiguation string `json:"disambiguation"`
		} `json:"artists"`
	}
	params := url.Values{"query": {`artist:"` + luceneEscape(name) + `"`}, "limit": {"10"}}
	if err := musicbrainz.get(ctx, "/artist/", params, &raw); err != nil {
		return "", "musicbrainz: " + err.Error()
	}
	var exact []string
	for _, ar := range raw.Artists {
		if normalizeName(ar.Name) == normalizeName(name) {
			exact = append(exact, ar.ID)
		}
	}
	switch len(exact) {
	case 0:
		return "", ""
	case 1:
		return exact[0], ""
	}
	return "", fmt.Sprintf("musicbrainz: hay %d artistas llamados «%s»; sin un disco suyo en la colección no sé cuál es", len(exact), name)
}

// mbArtistReleases: las ediciones oficiales del artista (discos y EPs),
// una por disco (grupo de ediciones): la más antigua.
func mbArtistReleases(ctx context.Context, mbid string) ([]discRelease, error) {
	type rawRelease struct {
		ID           string           `json:"id"`
		Title        string           `json:"title"`
		Date         string           `json:"date"`
		Country      string           `json:"country"`
		ArtistCredit []mbArtistCredit `json:"artist-credit"`
		Media        []struct {
			Format     string `json:"format"`
			TrackCount int    `json:"track-count"`
		} `json:"media"`
		ReleaseGroup struct {
			ID             string   `json:"id"`
			PrimaryType    string   `json:"primary-type"`
			SecondaryTypes []string `json:"secondary-types"`
			FirstRelease   string   `json:"first-release-date"`
		} `json:"release-group"`
	}
	byGroup := map[string]discRelease{}
	relDate := map[string]string{} // fecha de la edición elegida (Date lleva la del disco)
	var order []string
	for offset := 0; offset < 300; offset += 100 {
		var raw struct {
			Count    int          `json:"release-count"`
			Releases []rawRelease `json:"releases"`
		}
		params := url.Values{
			"artist": {mbid}, "status": {"official"}, "type": {"album|ep"},
			"inc": {"release-groups+media+artist-credits"}, "limit": {"100"}, "offset": {strconv.Itoa(offset)},
		}
		if err := musicbrainz.get(ctx, "/release", params, &raw); err != nil {
			if offset == 0 {
				return nil, err
			}
			break
		}
		for _, r := range raw.Releases {
			rg := r.ReleaseGroup
			kind := strings.ToLower(rg.PrimaryType)
			for _, t := range rg.SecondaryTypes {
				switch strings.ToLower(t) {
				case "compilation":
					kind = "compile"
				case "live":
					kind = "live"
				}
			}
			n := 0
			var formats []string
			for _, m := range r.Media {
				n += m.TrackCount
				if m.Format != "" {
					formats = append(formats, m.Format)
				}
			}
			rel := discRelease{
				mbRelease: mbRelease{
					ID: r.ID, Source: srcMusicBrainz, Title: r.Title, Date: firstNonEmpty(rg.FirstRelease, r.Date),
					Country: r.Country, TrackCount: n, Format: strings.Join(dedupe(formats), "+"),
					Artist: joinCredits(r.ArtistCredit), FirstArtist: firstCredit(r.ArtistCredit),
					FirstArtistID: firstCreditID(r.ArtistCredit), ReleaseGroupID: rg.ID,
				},
				Kind: kind,
			}
			_, ok := byGroup[rg.ID]
			if !ok {
				order = append(order, rg.ID)
			}
			// La edición original (la más antigua) representa al disco.
			if prev := relDate[rg.ID]; !ok || (r.Date != "" && (prev == "" || r.Date < prev)) {
				byGroup[rg.ID], relDate[rg.ID] = rel, r.Date
			}
		}
		if offset+100 >= raw.Count {
			break
		}
	}
	out := make([]discRelease, 0, len(order))
	for _, id := range order {
		out = append(out, byGroup[id])
	}
	return out, nil
}

// mergeDiscographies funde las dos listas por título: manda la de
// MusicBrainz (ediciones físicas, tracklist fiable) y de Deezer se añade lo
// que falte; si están en las dos, la de MusicBrainz se queda con la carátula
// de Deezer. Orden: por clase de disco y fecha.
func mergeDiscographies(mbz, dz []discRelease) []discRelease {
	out := make([]discRelease, 0, len(mbz)+len(dz))
	find := func(title string) int {
		for i, r := range out {
			if sameDiscTitle(r.Title, title) {
				return i
			}
		}
		return -1
	}
	for _, r := range mbz {
		if find(r.Title) < 0 {
			out = append(out, r)
		}
	}
	for _, r := range dz {
		if i := find(r.Title); i >= 0 {
			if out[i].CoverURL == "" {
				out[i].CoverURL = r.CoverURL
			}
			continue
		}
		out = append(out, r)
	}
	rank := map[string]int{"album": 0, "ep": 1, "live": 2, "compile": 3, "single": 4}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank[out[i].Kind], rank[out[j].Kind]; ri != rj {
			return ri < rj
		}
		return out[i].Date < out[j].Date
	})
	return out
}

// sameDiscTitle: el mismo disco escrito de dos maneras. Se comparan sin
// paréntesis y sin subtítulo («Sempiternal Past: The Darkthrone Demos»), con
// margen para erratas («Plagueweilder»).
func sameDiscTitle(a, b string) bool {
	core := func(s string) string {
		s = stripParen(s)
		if i := strings.Index(s, ":"); i > 2 {
			s = s[:i]
		}
		return s
	}
	return similarity(core(a), core(b)) >= 0.84
}

// ---- Last.fm --------------------------------------------------------------

var lastfmBase = "https://ws.audioscrobbler.com/2.0/" // lo cambian los tests

func lastfmSimilar(ctx context.Context, key, name, mbid string) ([]discArtist, error) {
	params := url.Values{"method": {"artist.getsimilar"}, "api_key": {key}, "format": {"json"}, "limit": {"30"}, "autocorrect": {"1"}}
	if mbid != "" {
		params.Set("mbid", mbid)
	} else {
		params.Set("artist", name)
	}
	var raw struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
		Similar struct {
			Artist []struct {
				Name  string `json:"name"`
				MBID  string `json:"mbid"`
				Match string `json:"match"`
			} `json:"artist"`
		} `json:"similarartists"`
	}
	if err := getJSON(ctx, http.MethodGet, lastfmBase+"?"+params.Encode(), nil, nil, &raw); err != nil {
		// Con MBID Last.fm a veces no conoce al artista: se reintenta por nombre.
		if mbid != "" && name != "" {
			return lastfmSimilar(ctx, key, name, "")
		}
		return nil, err
	}
	if raw.Error != 0 {
		if mbid != "" && name != "" {
			return lastfmSimilar(ctx, key, name, "")
		}
		return nil, fmt.Errorf("%s", raw.Message)
	}
	out := make([]discArtist, 0, len(raw.Similar.Artist))
	for _, s := range raw.Similar.Artist {
		m, _ := strconv.ParseFloat(s.Match, 64)
		out = append(out, discArtist{Name: s.Name, MBID: s.MBID, Match: m, Sources: []string{"lastfm"}})
	}
	return out, nil
}

// mergeSimilar junta las sugerencias de Last.fm y Deezer por nombre. Salir
// en las dos es buena señal: sube un poco. Se quita al propio artista.
func mergeSimilar(self string, lf, dz []discArtist, limit int) []discArtist {
	idx := map[string]int{}
	var out []discArtist
	add := func(ar discArtist) {
		k := normalizeName(ar.Name)
		if k == "" || k == normalizeName(self) {
			return
		}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, ar)
			return
		}
		cur := &out[i]
		cur.Match = min(1, max(cur.Match, ar.Match)+0.1)
		cur.Sources = append(cur.Sources, ar.Sources...)
		cur.MBID = firstNonEmpty(cur.MBID, ar.MBID)
		if cur.DeezerID == "" {
			cur.DeezerID, cur.Picture, cur.Fans = ar.DeezerID, ar.Picture, ar.Fans
		}
	}
	for _, ar := range lf {
		add(ar)
	}
	for _, ar := range dz {
		add(ar)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Match > out[j].Match })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
