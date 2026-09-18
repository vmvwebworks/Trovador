package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Identificar un disco por sus canciones. Cuando el nombre de la carpeta no
// sirve ("1998 - Demo", "((Nuevos))"...), se buscan 3-4 títulos de pista y
// se cuenta en qué ediciones aparecen: la que contenga más de ellos es la
// candidata. Luego pasa por la comprobación de duraciones como cualquier
// otra.

var (
	genericTitle = regexp.MustCompile(`(?i)^(intro|outro|untitled|track ?\d+|pista ?\d+|bonus)$`)
	anyParen     = regexp.MustCompile(`\s*[(\[][^)\]]*[)\]]`)
)

// pickTitles elige hasta n títulos distintos y distintivos (los más largos
// primero: "Ahi Amours Con Dure Departie" identifica mejor que "Intro").
func pickTitles(files []localTrack, n int) []string {
	var titles []string
	seen := map[string]bool{}
	for _, f := range files {
		t := trackTitleOf(f)
		// "Ahi Amours (3rd crusade 1188)": el paréntesis es un apunte del que
		// etiquetó, no parte del título; se quita si queda algo sustancial.
		if bare := strings.TrimSpace(anyParen.ReplaceAllString(t, " ")); len(normalizeName(bare)) >= 4 {
			t = spaces.ReplaceAllString(bare, " ")
		}
		key := normalizeName(t)
		if len(key) < 4 || genericTitle.MatchString(t) || seen[key] {
			continue
		}
		seen[key] = true
		titles = append(titles, t)
	}
	sort.SliceStable(titles, func(i, j int) bool { return len(titles[i]) > len(titles[j]) })
	if len(titles) > n {
		titles = titles[:n]
	}
	return titles
}

// searchByTracks busca los títulos en las fuentes y devuelve las ediciones
// ordenadas por cuántos de esos títulos contienen (campo Hits).
func searchByTracks(ctx context.Context, artist string, files []localTrack, opts searchOptions) (results []mbRelease, warnings []string) {
	titles := pickTitles(files, 4)
	if len(titles) == 0 {
		return nil, []string{"no hay títulos de pista utilizables"}
	}

	type tally struct {
		rel    mbRelease
		titles map[string]bool // qué títulos buscados aparecen en esta edición
	}
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		found = map[string]*tally{}
	)
	hit := func(title string, rels []mbRelease) {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range rels {
			t, ok := found[r.ID]
			if !ok {
				t = &tally{rel: r, titles: map[string]bool{}}
				found[r.ID] = t
			}
			t.titles[title] = true
		}
	}
	warn := func(name string, err error) {
		mu.Lock()
		warnings = append(warnings, name+": "+err.Error())
		mu.Unlock()
	}

	// MusicBrainz va en serie (1 req/s); Deezer y Discogs, en paralelo.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, title := range titles {
			rels, err := musicbrainz.searchRecordings(ctx, artist, title)
			if err != nil {
				warn(srcMusicBrainz, err)
				continue // un título que falla no invalida los demás
			}
			hit(title, rels)
		}
	}()
	for _, title := range titles {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			rels, err := deezerTrackSearch(ctx, artist, title)
			if err != nil {
				warn(srcDeezer, err)
				return
			}
			hit(title, rels)
		}(title)
		if opts.DiscogsToken != "" {
			wg.Add(1)
			go func(title string) {
				defer wg.Done()
				rels, err := discogsTrackSearch(ctx, artist, title, opts.DiscogsToken)
				if err != nil {
					warn(srcDiscogs, err)
					return
				}
				hit(title, rels)
			}(title)
		}
	}
	wg.Wait()

	for _, t := range found {
		r := t.rel
		r.Hits = len(t.titles)
		r.HitsOf = len(titles)
		results = append(results, r)
	}
	// Más coincidencias primero; a igualdad, la fuente de referencia y su
	// puntuación.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Hits != results[j].Hits {
			return results[i].Hits > results[j].Hits
		}
		if (results[i].Source == srcMusicBrainz) != (results[j].Source == srcMusicBrainz) {
			return results[i].Source == srcMusicBrainz
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > 15 {
		results = results[:15]
	}
	return results, dedupe(warnings)
}

// searchRecordings busca grabaciones por título (y artista) y devuelve las
// ediciones en las que aparecen.
func (c *mbClient) searchRecordings(ctx context.Context, artist, title string) ([]mbRelease, error) {
	q := `recording:"` + luceneEscape(title) + `"`
	if artist = strings.TrimSpace(artist); artist != "" {
		q += ` AND artist:"` + luceneEscape(artist) + `"`
	}
	var raw struct {
		Recordings []struct {
			Score        int              `json:"score"`
			ArtistCredit []mbArtistCredit `json:"artist-credit"`
			Releases     []struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				Date         string `json:"date"`
				Country      string `json:"country"`
				TrackCount   int    `json:"track-count"`
				ReleaseGroup struct {
					ID string `json:"id"`
				} `json:"release-group"`
				Media []struct {
					Format string `json:"format"`
				} `json:"media"`
			} `json:"releases"`
		} `json:"recordings"`
	}
	if err := c.get(ctx, "/recording/", url.Values{"query": {q}, "limit": {"10"}}, &raw); err != nil {
		return nil, err
	}
	var out []mbRelease
	seen := map[string]bool{}
	for _, rec := range raw.Recordings {
		for _, r := range rec.Releases {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			var formats []string
			for _, m := range r.Media {
				if m.Format != "" {
					formats = append(formats, m.Format)
				}
			}
			out = append(out, mbRelease{
				ID: r.ID, Source: srcMusicBrainz, Title: r.Title, Artist: joinCredits(rec.ArtistCredit),
				Date: r.Date, Country: r.Country, TrackCount: r.TrackCount,
				Format: strings.Join(dedupe(formats), "+"), ReleaseGroupID: r.ReleaseGroup.ID, Score: rec.Score,
			})
		}
	}
	return out, nil
}

// deezerTrackSearch: pistas que se llaman así; nos quedamos con su álbum.
func deezerTrackSearch(ctx context.Context, artist, title string) ([]mbRelease, error) {
	var raw struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				ID      int64  `json:"id"`
				Title   string `json:"title"`
				CoverXL string `json:"cover_xl"`
			} `json:"album"`
		} `json:"data"`
	}
	q := strings.TrimSpace(artist + " " + title)
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/search/track?limit=8&q="+url.QueryEscape(q), nil, nil, &raw); err != nil {
		return nil, err
	}
	var out []mbRelease
	seen := map[int64]bool{}
	for _, t := range raw.Data {
		// Solo si el título realmente se parece: la búsqueda de Deezer es laxa.
		if similarity(t.Title, title) < 0.6 || seen[t.Album.ID] {
			continue
		}
		if artist != "" && similarity(t.Artist.Name, artist) < 0.5 {
			continue
		}
		seen[t.Album.ID] = true
		out = append(out, mbRelease{
			ID: sourceKey(srcDeezer, strconv.FormatInt(t.Album.ID, 10)), Source: srcDeezer,
			Title: t.Album.Title, Artist: t.Artist.Name, Format: "Digital", CoverURL: t.Album.CoverXL, Score: 50,
		})
	}
	return out, nil
}

// discogsTrackSearch usa el filtro "track" del buscador de Discogs.
func discogsTrackSearch(ctx context.Context, artist, title, token string) ([]mbRelease, error) {
	params := url.Values{"type": {"release"}, "per_page": {"8"}, "token": {token}, "track": {title}}
	if artist != "" {
		params.Set("artist", artist)
	}
	var raw struct {
		Results []struct {
			ID      int64    `json:"id"`
			Title   string   `json:"title"`
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
		art, t, _ := strings.Cut(r.Title, " - ")
		if t == "" {
			art, t = "", r.Title
		}
		out = append(out, mbRelease{
			ID: sourceKey(srcDiscogs, strconv.FormatInt(r.ID, 10)), Source: srcDiscogs,
			Title: t, Artist: art, Date: r.Year, Country: r.Country,
			Format: strings.Join(dedupe(r.Format), "+"), CoverURL: r.Cover, Score: 50,
		})
	}
	return out, nil
}

// SearchByTracks: identificar el disco de una carpeta por sus canciones.
func (a *App) SearchByTracks(dir string) (searchResult, error) {
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return searchResult{}, err
	}
	if len(scan.Files) == 0 {
		return searchResult{}, fmt.Errorf("la carpeta no tiene ficheros de audio")
	}
	results, warnings := searchByTracks(a.ctx, commonArtist(scan.Files), scan.Files, a.searchOpts(false))
	return searchResult{Results: results, Warnings: warnings}, nil
}

// SearchBySong: en qué discos aparece una canción. Reutiliza la búsqueda por
// pistas con un único título.
func (a *App) SearchBySong(artist, title string) (searchResult, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return searchResult{}, fmt.Errorf("escribe el título de la canción")
	}
	results, warnings := searchByTracks(a.ctx, strings.TrimSpace(artist), []localTrack{{Name: title + ".mp3", Title: title}}, a.searchOpts(false))
	if len(results) == 0 && artist != "" {
		// El "artista" puede ser el compositor o estar mal escrito: sin él.
		results, warnings = searchByTracks(a.ctx, "", []localTrack{{Name: title + ".mp3", Title: title}}, a.searchOpts(false))
	}
	return searchResult{Results: results, Warnings: warnings}, nil
}
