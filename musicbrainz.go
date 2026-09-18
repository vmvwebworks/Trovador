package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cliente de MusicBrainz (https://musicbrainz.org/doc/MusicBrainz_API): la
// base de datos abierta de referencia para ediciones, tracklists y
// duraciones. Y de Cover Art Archive, su archivo de carátulas.
//
// Reglas de uso de MusicBrainz: identificarse con un User-Agent propio y no
// pasar de 1 petición por segundo. Lo segundo lo garantiza mbClient.get.

const (
	mbBase    = "https://musicbrainz.org/ws/2"
	caaBase   = "https://coverartarchive.org"
	userAgent = "Trovador/0.3 (app de escritorio personal; Go net/http)"
)

type mbClient struct {
	mu   sync.Mutex // serializa las peticiones para respetar el límite
	last time.Time
	http *http.Client
}

var musicbrainz = &mbClient{http: &http.Client{Timeout: 60 * time.Second}}

// get hace una petición JSON a MusicBrainz y decodifica la respuesta en v.
// `v any` + json.Decoder es la forma habitual en Go de "dame un JSON en
// este struct que te paso".
func (c *mbClient) get(ctx context.Context, path string, params url.Values, v any) error {
	c.mu.Lock()
	if wait := time.Second - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
	c.mu.Unlock()

	params.Set("fmt", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mbBase+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	// MusicBrainz devuelve 503 "busy" con cierta frecuencia: se reintenta
	// con espera creciente (1 s, 2 s, 3 s) antes de rendirse.
	for attempt := 1; ; attempt++ {
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusServiceUnavailable && attempt < 4 {
			resp.Body.Close()
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return fmt.Errorf("MusicBrainz %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return json.NewDecoder(resp.Body).Decode(v)
	}
}

// mbRelease es una edición concreta de un disco (un CD de 1996, una
// reedición en vinilo de 2010...). Cada una tiene su propia tracklist.
type mbRelease struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Artist         string `json:"artist"`
	FirstArtist    string `json:"firstArtist"`   // el primero de los acreditados, cuando la fuente los da por separado
	FirstArtistID  string `json:"firstArtistId"` // su MBID (solo MusicBrainz)
	Date           string `json:"date"`
	Country        string `json:"country"`
	TrackCount     int    `json:"trackCount"`
	Format         string `json:"format"` // CD, Vinyl, Digital Media...
	ReleaseGroupID string `json:"releaseGroupId"`
	Score          int    `json:"score"`
	Source         string `json:"source"`      // musicbrainz | deezer | bandcamp | discogs
	CoverURL       string `json:"coverUrl"`    // carátula directa (fuentes que la dan); MusicBrainz usa Cover Art Archive
	Hits           int    `json:"hits"`        // búsqueda por pistas: cuántos títulos buscados contiene
	HitsOf         int    `json:"hitsOf"`      // ...de cuántos se buscaron
	LengthsFrom    string `json:"lengthsFrom"` // fuente de la que se tomaron las duraciones si esta no las traía
}

// Structs "crudos" con la forma exacta del JSON de MusicBrainz. Los
// separamos de los que exponemos a la ventana para no arrastrar su
// estructura (artist-credit con joinphrase, media anidados...).
type mbArtistCredit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
	Artist     struct {
		ID string `json:"id"`
	} `json:"artist"`
}

func (ac mbArtistCredit) String() string { return ac.Name + ac.JoinPhrase }

func joinCredits(credits []mbArtistCredit) string {
	var b strings.Builder
	for _, c := range credits {
		b.WriteString(c.String())
	}
	return b.String()
}

// firstCredit: el primer artista acreditado ("Oliphant" de "Oliphant / Sequentia").
func firstCredit(credits []mbArtistCredit) string {
	if len(credits) == 0 {
		return ""
	}
	return strings.TrimSpace(credits[0].Name)
}

func firstCreditID(credits []mbArtistCredit) string {
	if len(credits) == 0 {
		return ""
	}
	return credits[0].Artist.ID
}

// searchReleases busca ediciones por artista y título. La consulta usa la
// sintaxis Lucene de MusicBrainz: artist:"..." AND release:"..." (frase
// exacta) o, en modo fuzzy, palabras sueltas con tolerancia a erratas
// (release:(endzeit~1 barbarossa~1)), que también ignora acentos.
func (c *mbClient) searchReleases(ctx context.Context, artist, album string, fuzzy bool) ([]mbRelease, error) {
	var parts []string
	field := func(name, value string) string {
		if !fuzzy {
			return name + `:"` + luceneEscape(value) + `"`
		}
		return name + ":(" + luceneFuzzyTerms(value) + ")"
	}
	if artist = strings.TrimSpace(artist); artist != "" {
		parts = append(parts, field("artist", artist))
	}
	if album = strings.TrimSpace(album); album != "" {
		parts = append(parts, field("release", album))
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("indica artista o álbum")
	}

	var raw struct {
		Releases []struct {
			ID           string           `json:"id"`
			Title        string           `json:"title"`
			Date         string           `json:"date"`
			Country      string           `json:"country"`
			Score        int              `json:"score"`
			TrackCount   int              `json:"track-count"`
			ArtistCredit []mbArtistCredit `json:"artist-credit"`
			Media        []struct {
				Format string `json:"format"`
			} `json:"media"`
			ReleaseGroup struct {
				ID string `json:"id"`
			} `json:"release-group"`
		} `json:"releases"`
	}
	params := url.Values{"query": {strings.Join(parts, " AND ")}, "limit": {"20"}}
	if err := c.get(ctx, "/release/", params, &raw); err != nil {
		return nil, err
	}

	out := make([]mbRelease, 0, len(raw.Releases))
	for _, r := range raw.Releases {
		rel := mbRelease{
			ID: r.ID, Source: srcMusicBrainz, Title: r.Title, Date: r.Date, Country: r.Country, Score: r.Score,
			TrackCount:     r.TrackCount,
			Artist:         joinCredits(r.ArtistCredit),
			FirstArtist:    firstCredit(r.ArtistCredit),
			FirstArtistID:  firstCreditID(r.ArtistCredit),
			ReleaseGroupID: r.ReleaseGroup.ID,
		}
		var formats []string
		for _, m := range r.Media {
			if m.Format != "" {
				formats = append(formats, m.Format)
			}
		}
		rel.Format = strings.Join(dedupe(formats), "+")
		out = append(out, rel)
	}
	return out, nil
}

func luceneEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// luceneFuzzyTerms convierte "Endzeit Barbarossa" en "+endzeit~1 +barbarossa~1":
// las palabras significativas (4+ letras) son obligatorias (+) y admiten una
// letra de diferencia (~1); las cortas ("et", "du", "les") quedan opcionales,
// porque si no cualquier disco francés coincide. Al normalizar se pierden
// los acentos, que MusicBrainz ignora igual.
func luceneFuzzyTerms(s string) string {
	words := strings.Fields(normalizeName(s))
	// Obligatorias: como mucho las 4 palabras más largas de 5+ letras; los
	// números romanos ("xiie", "xiii") y las cortas, opcionales.
	var long []string
	for _, w := range words {
		if len([]rune(w)) >= 5 && !romanRe.MatchString(w) {
			long = append(long, w)
		}
	}
	sort.SliceStable(long, func(i, j int) bool { return len(long[i]) > len(long[j]) })
	required := map[string]bool{}
	for i, w := range long {
		if i < 4 {
			required[w] = true
		}
	}
	var terms []string
	for _, w := range words {
		switch {
		case required[w]:
			terms = append(terms, "+"+w+"~1")
		case len([]rune(w)) >= 4:
			terms = append(terms, w+"~1")
		default:
			terms = append(terms, w)
		}
	}
	return strings.Join(terms, " ")
}

var romanRe = regexp.MustCompile(`^[ivxlcdm]+(e|eme|th|st|nd|rd)?$`)

// stripParen quita los paréntesis y corchetes de un título de disco:
// "Anthologie chantée des Troubadours (XIIe & XIIIe siècles)" -> "Anthologie
// chantée des Troubadours". Casi nunca forman parte del título canónico.
func stripParen(s string) string {
	out := strings.TrimSpace(anyParen.ReplaceAllString(s, " "))
	out = spaces.ReplaceAllString(out, " ")
	if len(normalizeName(out)) < 3 {
		return s
	}
	return out
}

// mbTrack es una pista de la tracklist oficial.
type mbTrack struct {
	Number   int     `json:"number"`   // correlativo en todo el disco (1..N)
	Position int     `json:"position"` // posición dentro de su medio (CD1, CD2...)
	Medium   int     `json:"medium"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Length   float64 `json:"length"`        // segundos; 0 si MusicBrainz no la tiene
	URL      string  `json:"url,omitempty"` // Bandcamp: la página de la pista (se puede descargar con yt-dlp)
}

type mbReleaseDetail struct {
	Release     mbRelease `json:"release"`
	Tracks      []mbTrack `json:"tracks"`
	TotalLength float64   `json:"totalLength"`
}

// getRelease trae la tracklist completa de una edición.
func (c *mbClient) getRelease(ctx context.Context, id string) (mbReleaseDetail, error) {
	var raw struct {
		ID           string           `json:"id"`
		Title        string           `json:"title"`
		Date         string           `json:"date"`
		Country      string           `json:"country"`
		ArtistCredit []mbArtistCredit `json:"artist-credit"`
		ReleaseGroup struct {
			ID string `json:"id"`
		} `json:"release-group"`
		Media []struct {
			Format string `json:"format"`
			Tracks []struct {
				Position     int              `json:"position"`
				Title        string           `json:"title"`
				Length       int              `json:"length"` // milisegundos
				ArtistCredit []mbArtistCredit `json:"artist-credit"`
				Recording    struct {
					Length int `json:"length"`
				} `json:"recording"`
			} `json:"tracks"`
		} `json:"media"`
	}
	params := url.Values{"inc": {"recordings+artist-credits+release-groups"}}
	if err := c.get(ctx, "/release/"+url.PathEscape(id), params, &raw); err != nil {
		return mbReleaseDetail{}, err
	}

	d := mbReleaseDetail{Release: mbRelease{
		ID: raw.ID, Source: srcMusicBrainz, Title: raw.Title, Date: raw.Date, Country: raw.Country,
		Artist:         joinCredits(raw.ArtistCredit),
		FirstArtist:    firstCredit(raw.ArtistCredit),
		FirstArtistID:  firstCreditID(raw.ArtistCredit),
		ReleaseGroupID: raw.ReleaseGroup.ID,
	}}
	n := 0
	for mi, m := range raw.Media {
		if m.Format != "" && d.Release.Format == "" {
			d.Release.Format = m.Format
		}
		for _, t := range m.Tracks {
			n++
			ms := t.Length
			if ms == 0 {
				ms = t.Recording.Length
			}
			track := mbTrack{
				Number: n, Position: t.Position, Medium: mi + 1,
				Title:  t.Title,
				Artist: joinCredits(t.ArtistCredit),
				Length: float64(ms) / 1000,
			}
			d.TotalLength += track.Length
			d.Tracks = append(d.Tracks, track)
		}
	}
	d.Release.TrackCount = n
	return d, nil
}

// fetchCover descarga la carátula frontal (500 px) de Cover Art Archive.
// Primero la de esa edición; si no tiene, la del grupo (el "disco" en
// abstracto, que agrupa todas sus ediciones).
func fetchCover(ctx context.Context, releaseID, releaseGroupID, dest string) error {
	candidates := []string{caaBase + "/release/" + releaseID + "/front-500"}
	if releaseGroupID != "" {
		candidates = append(candidates, caaBase+"/release-group/"+releaseGroupID+"/front-500")
	}

	client := &http.Client{Timeout: 60 * time.Second}
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req) // sigue las redirecciones a archive.org solo
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}
		f, err := os.Create(dest)
		if err != nil {
			resp.Body.Close()
			return err
		}
		_, err = io.Copy(f, resp.Body)
		resp.Body.Close()
		f.Close()
		return err
	}
	return fmt.Errorf("Cover Art Archive no tiene carátula para esta edición")
}
