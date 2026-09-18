package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Varias fuentes de información de discos, consultadas a la vez desde la
// app. Todas se convierten al mismo par de structs (mbRelease para la lista
// de candidatas, mbReleaseDetail para la tracklist), y cada candidata lleva
// su fuente y un identificador compuesto "fuente:id" para poder volver a
// pedirle el detalle a quien corresponda.
//
//   - musicbrainz: la referencia; sin clave; 1 req/s (musicbrainz.go)
//   - deezer: API pública sin clave; duraciones y carátulas grandes
//   - bandcamp: su autocompletado JSON + la página del álbum (tracklist)
//   - discogs: la mejor para demos y tiradas pequeñas; necesita token
//     personal (gratuito: discogs.com > Settings > Developers)

const (
	srcMusicBrainz = "musicbrainz"
	srcDeezer      = "deezer"
	srcBandcamp    = "bandcamp"
	srcDiscogs     = "discogs"
)

// sourceKey y parseKey convierten entre (fuente, id) y "fuente:id". Los IDs
// de MusicBrainz van sin prefijo por compatibilidad (son UUID únicos).
func sourceKey(source, id string) string {
	if source == srcMusicBrainz || source == "" {
		return id
	}
	return source + ":" + id
}

func parseKey(key string) (source, id string) {
	for _, s := range []string{srcDeezer, srcBandcamp, srcDiscogs} {
		if rest, ok := strings.CutPrefix(key, s+":"); ok {
			return s, rest
		}
	}
	return srcMusicBrainz, key
}

var webClient = &http.Client{Timeout: 30 * time.Second}

// getJSON hace una petición y decodifica la respuesta JSON en v.
func getJSON(ctx context.Context, method, u string, body []byte, headers map[string]string, v any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := webClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// ---- búsqueda en todas las fuentes ----------------------------------------

// searchOptions controla qué fuentes se consultan.
type searchOptions struct {
	Fuzzy        bool   // añadir la búsqueda aproximada de MusicBrainz
	DiscogsToken string // vacío = Discogs desactivado
}

// searchAll consulta las fuentes en paralelo y junta los resultados. Los
// fallos de una fuente no tumban la búsqueda: se devuelven aparte para
// mostrarlos como aviso.
func searchAll(ctx context.Context, artist, album string, opts searchOptions) (results []mbRelease, warnings []string) {
	type out struct {
		rels []mbRelease
		err  error
		name string
	}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
		by = map[string][]mbRelease{}
	)
	run := func(name string, fn func() ([]mbRelease, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rels, err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, name+": "+err.Error())
				return
			}
			by[name] = append(by[name], rels...)
		}()
	}

	// Los paréntesis ("(XIIe & XIIIe siècles)", "(Deluxe)") casi nunca forman
	// parte del título canónico: se busca sin ellos. MusicBrainz prueba primero
	// la frase completa por si acaso.
	short := stripParen(album)
	run(srcMusicBrainz, func() ([]mbRelease, error) {
		rels, err := musicbrainz.searchReleases(ctx, artist, album, false)
		if err != nil {
			return nil, err
		}
		if len(rels) == 0 && short != album {
			rels, _ = musicbrainz.searchReleases(ctx, artist, short, false)
		}
		if opts.Fuzzy {
			more, err := musicbrainz.searchReleases(ctx, artist, short, true)
			if err == nil {
				rels = append(rels, more...)
			}
		}
		return rels, nil
	})
	run(srcDeezer, func() ([]mbRelease, error) { return searchDeezer(ctx, artist, short) })
	run(srcBandcamp, func() ([]mbRelease, error) { return searchBandcamp(ctx, artist, short) })
	if opts.DiscogsToken != "" {
		run(srcDiscogs, func() ([]mbRelease, error) { return searchDiscogs(ctx, artist, short, opts.DiscogsToken) })
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, name := range []string{srcMusicBrainz, srcDiscogs, srcBandcamp, srcDeezer} {
		for _, r := range by[name] {
			if !seen[r.ID] {
				seen[r.ID] = true
				results = append(results, r)
			}
		}
	}
	return results, warnings
}

// getDetail pide la tracklist a la fuente que corresponda al identificador.
func getDetail(ctx context.Context, key, discogsToken string) (mbReleaseDetail, error) {
	source, id := parseKey(key)
	switch source {
	case srcDeezer:
		return deezerDetail(ctx, id)
	case srcBandcamp:
		return bandcampDetail(ctx, id)
	case srcDiscogs:
		return discogsDetail(ctx, id, discogsToken)
	default:
		return musicbrainz.getRelease(ctx, id)
	}
}

// coverFor descarga la carátula de una edición a dest, de donde venga.
// coverFor baja la carátula de la edición: la que da su fuente (Deezer,
// Bandcamp, Discogs) o, para MusicBrainz, la de Cover Art Archive. Si su
// fuente no la tiene, se busca el mismo disco en las demás fuentes y se
// toma la de la primera que cuadre. Devuelve de dónde salió.
func coverFor(ctx context.Context, d mbReleaseDetail, dest string, opts searchOptions) (string, error) {
	if d.Release.CoverURL != "" {
		if err := downloadTo(ctx, d.Release.CoverURL, dest); err == nil {
			return d.Release.Source, nil
		}
	}
	if d.Release.Source == srcMusicBrainz {
		if err := fetchCover(ctx, d.Release.ID, d.Release.ReleaseGroupID, dest); err == nil {
			return "Cover Art Archive", nil
		}
	}
	// Otra fuente con el mismo disco (mismo título y artista; a igualdad,
	// mismo número de pistas).
	rels, _ := searchAll(ctx, d.Release.Artist, stripParen(d.Release.Title), opts)
	sort.SliceStable(rels, func(i, j int) bool {
		return (rels[i].TrackCount == len(d.Tracks)) && (rels[j].TrackCount != len(d.Tracks))
	})
	for _, r := range rels {
		if r.Source == d.Release.Source || r.CoverURL == "" {
			continue
		}
		if similarity(r.Title, d.Release.Title) < 0.85 || (d.Release.Artist != "" && similarity(r.Artist, d.Release.Artist) < 0.8) {
			continue
		}
		if err := downloadTo(ctx, r.CoverURL, dest); err == nil {
			return r.Source, nil
		}
	}
	return "", fmt.Errorf("ninguna fuente tiene carátula de este disco")
}

func downloadTo(ctx context.Context, u, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := webClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("carátula: HTTP %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// ---- Deezer ---------------------------------------------------------------

func searchDeezer(ctx context.Context, artist, album string) ([]mbRelease, error) {
	q := strings.TrimSpace(artist + " " + album)
	if artist != "" && album != "" {
		q = fmt.Sprintf(`artist:"%s" album:"%s"`, artist, album)
	}
	var raw struct {
		Data []struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			NbTracks int    `json:"nb_tracks"`
			CoverXL  string `json:"cover_xl"`
			Type     string `json:"record_type"`
			Artist   struct {
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"data"`
	}
	u := "https://api.deezer.com/search/album?limit=10&q=" + url.QueryEscape(q)
	if err := getJSON(ctx, http.MethodGet, u, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]mbRelease, 0, len(raw.Data))
	for _, a := range raw.Data {
		out = append(out, mbRelease{
			ID: sourceKey(srcDeezer, strconv.FormatInt(a.ID, 10)), Source: srcDeezer,
			Title: a.Title, Artist: a.Artist.Name, TrackCount: a.NbTracks,
			Format: "Digital (" + a.Type + ")", CoverURL: a.CoverXL, Score: 50,
		})
	}
	return out, nil
}

func deezerDetail(ctx context.Context, id string) (mbReleaseDetail, error) {
	var album struct {
		Title       string `json:"title"`
		ReleaseDate string `json:"release_date"`
		CoverXL     string `json:"cover_xl"`
		NbTracks    int    `json:"nb_tracks"`
		Artist      struct {
			Name string `json:"name"`
		} `json:"artist"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/album/"+id, nil, nil, &album); err != nil {
		return mbReleaseDetail{}, err
	}
	var tracks struct {
		Data []struct {
			Title    string  `json:"title"`
			Duration float64 `json:"duration"`
			Position int     `json:"track_position"`
			Disk     int     `json:"disk_number"`
			Artist   struct {
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"data"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/album/"+id+"/tracks?limit=200", nil, nil, &tracks); err != nil {
		return mbReleaseDetail{}, err
	}
	d := mbReleaseDetail{Release: mbRelease{
		ID: sourceKey(srcDeezer, id), Source: srcDeezer, Title: album.Title, Artist: album.Artist.Name,
		Date: album.ReleaseDate, Format: "Digital", CoverURL: album.CoverXL,
	}}
	for i, t := range tracks.Data {
		tr := mbTrack{Number: i + 1, Position: t.Position, Medium: t.Disk, Title: t.Title, Artist: t.Artist.Name, Length: t.Duration}
		d.TotalLength += tr.Length
		d.Tracks = append(d.Tracks, tr)
	}
	d.Release.TrackCount = len(d.Tracks)
	return d, nil
}

// ---- Bandcamp -------------------------------------------------------------

func searchBandcamp(ctx context.Context, artist, album string) ([]mbRelease, error) {
	body, _ := json.Marshal(map[string]any{
		"search_text": strings.TrimSpace(artist + " " + album), "search_filter": "a", "full_page": false, "fan_id": nil,
	})
	var raw struct {
		Auto struct {
			Results []struct {
				Type     string `json:"type"`
				Name     string `json:"name"`
				BandName string `json:"band_name"`
				URL      string `json:"item_url_path"`
				Img      string `json:"img"`
			} `json:"results"`
		} `json:"auto"`
	}
	err := getJSON(ctx, http.MethodPost, "https://bandcamp.com/api/bcsearch_public_api/1/autocomplete_elastic",
		body, map[string]string{"Content-Type": "application/json"}, &raw)
	if err != nil {
		return nil, err
	}
	var out []mbRelease
	for _, r := range raw.Auto.Results {
		if r.Type != "a" || r.URL == "" {
			continue
		}
		out = append(out, mbRelease{
			ID: sourceKey(srcBandcamp, r.URL), Source: srcBandcamp,
			Title: r.Name, Artist: r.BandName, Format: "Bandcamp", CoverURL: bigBandcampImage(r.Img), Score: 50,
		})
	}
	return out, nil
}

// bigBandcampImage: las miniaturas acaban en "_3.jpg"; "_10.jpg" es la grande.
func bigBandcampImage(u string) string {
	return regexp.MustCompile(`_\d+\.jpg$`).ReplaceAllString(u, "_10.jpg")
}

var (
	tralbumRe = regexp.MustCompile(`data-tralbum="([^"]+)"`)
	ogImageRe = regexp.MustCompile(`property="og:image" content="([^"]+)"`)
)

// bandcampDetail lee la página del álbum: lleva la tracklist con duraciones
// en un atributo JSON (data-tralbum) que es lo mismo que usa yt-dlp.
func bandcampDetail(ctx context.Context, albumURL string) (mbReleaseDetail, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, albumURL, nil)
	if err != nil {
		return mbReleaseDetail{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+userAgent)
	resp, err := webClient.Do(req)
	if err != nil {
		return mbReleaseDetail{}, err
	}
	defer resp.Body.Close()
	page, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return mbReleaseDetail{}, err
	}
	m := tralbumRe.FindSubmatch(page)
	if m == nil {
		return mbReleaseDetail{}, fmt.Errorf("no encuentro la tracklist en la página de Bandcamp")
	}
	var tr struct {
		Artist  string `json:"artist"`
		Current struct {
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
		} `json:"current"`
		TrackInfo []struct {
			Title    string  `json:"title"`
			Duration float64 `json:"duration"`
			TrackNum int     `json:"track_num"`
			Link     string  `json:"title_link"`
		} `json:"trackinfo"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &tr); err != nil {
		return mbReleaseDetail{}, fmt.Errorf("tracklist de Bandcamp: %w", err)
	}
	d := mbReleaseDetail{Release: mbRelease{
		ID: sourceKey(srcBandcamp, albumURL), Source: srcBandcamp, Title: tr.Current.Title, Artist: tr.Artist,
		Date: bandcampYear(tr.Current.ReleaseDate), Format: "Bandcamp",
	}}
	if og := ogImageRe.FindSubmatch(page); og != nil {
		d.Release.CoverURL = string(og[1])
	}
	for i, t := range tr.TrackInfo {
		track := mbTrack{Number: i + 1, Position: t.TrackNum, Medium: 1, Title: t.Title, Length: t.Duration, URL: bandcampTrackURL(albumURL, t.Link)}
		d.TotalLength += track.Length
		d.Tracks = append(d.Tracks, track)
	}
	d.Release.TrackCount = len(d.Tracks)
	return d, nil
}

// bandcampYear: "16 Sep 2010 00:00:00 GMT" -> "2010".
func bandcampYear(s string) string {
	if t, err := time.Parse("02 Jan 2006 15:04:05 MST", s); err == nil {
		return t.Format("2006")
	}
	return ""
}

// ---- Discogs --------------------------------------------------------------

func searchDiscogs(ctx context.Context, artist, album, token string) ([]mbRelease, error) {
	params := url.Values{"type": {"release"}, "per_page": {"15"}, "token": {token}}
	if artist != "" {
		params.Set("artist", artist)
	}
	if album != "" {
		params.Set("release_title", album)
	}
	if artist == "" || album == "" {
		params.Set("q", strings.TrimSpace(artist+" "+album))
	}
	var raw struct {
		Results []struct {
			ID      int64    `json:"id"`
			Title   string   `json:"title"` // "Artista - Título"
			Year    string   `json:"year"`
			Country string   `json:"country"`
			Format  []string `json:"format"`
			Cover   string   `json:"cover_image"`
		} `json:"results"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://api.discogs.com/database/search?"+params.Encode(), nil, nil, &raw); err != nil {
		return nil, err
	}
	var out []mbRelease
	for _, r := range raw.Results {
		art, title, _ := strings.Cut(r.Title, " - ")
		if title == "" {
			art, title = "", r.Title
		}
		out = append(out, mbRelease{
			ID: sourceKey(srcDiscogs, strconv.FormatInt(r.ID, 10)), Source: srcDiscogs,
			Title: title, Artist: art, Date: r.Year, Country: r.Country,
			Format: strings.Join(dedupe(r.Format), "+"), CoverURL: r.Cover, Score: 50,
		})
	}
	return out, nil
}

func discogsDetail(ctx context.Context, id, token string) (mbReleaseDetail, error) {
	var raw struct {
		Title   string `json:"title"`
		Year    int    `json:"year"`
		Country string `json:"country"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Formats []struct {
			Name string `json:"name"`
		} `json:"formats"`
		Images []struct {
			URI  string `json:"uri"`
			Type string `json:"type"`
		} `json:"images"`
		Tracklist []struct {
			Position string `json:"position"`
			Title    string `json:"title"`
			Duration string `json:"duration"` // "3:45", a veces vacío
			Type     string `json:"type_"`    // "track" o "heading"
		} `json:"tracklist"`
	}
	u := "https://api.discogs.com/releases/" + id + "?token=" + url.QueryEscape(token)
	if err := getJSON(ctx, http.MethodGet, u, nil, nil, &raw); err != nil {
		return mbReleaseDetail{}, err
	}
	d := mbReleaseDetail{Release: mbRelease{ID: sourceKey(srcDiscogs, id), Source: srcDiscogs, Title: raw.Title, Country: raw.Country}}
	if raw.Year > 0 {
		d.Release.Date = strconv.Itoa(raw.Year)
	}
	var names, formats []string
	for _, a := range raw.Artists {
		names = append(names, discogsName(a.Name))
	}
	for _, f := range raw.Formats {
		formats = append(formats, f.Name)
	}
	d.Release.Artist = strings.Join(names, ", ")
	if len(names) > 0 {
		d.Release.FirstArtist = names[0]
	}
	d.Release.Format = strings.Join(dedupe(formats), "+")
	for _, img := range raw.Images {
		if img.Type == "primary" || d.Release.CoverURL == "" {
			d.Release.CoverURL = img.URI
		}
	}
	n := 0
	for _, t := range raw.Tracklist {
		if t.Type != "" && t.Type != "track" {
			continue
		}
		n++
		track := mbTrack{Number: n, Position: n, Medium: 1, Title: t.Title, Length: parseClock(t.Duration)}
		d.TotalLength += track.Length
		d.Tracks = append(d.Tracks, track)
	}
	d.Release.TrackCount = n
	return d, nil
}

// discogsName quita el sufijo de desambiguación "(2)" que usa Discogs.
func discogsName(s string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s*\(\d+\)$`).ReplaceAllString(s, ""))
}

// parseClock: "3:45" o "1:02:03" -> segundos; 0 si vacío o raro.
func parseClock(s string) float64 {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if s == "" || len(parts) > 3 {
		return 0
	}
	total := 0.0
	for _, p := range parts {
		n, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}
