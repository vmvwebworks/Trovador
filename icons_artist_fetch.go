package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// De dónde sale la imagen del icono de una carpeta de artista: el logo en
// Metal Archives o TheAudioDB, o una foto en Deezer, Bandcamp o Discogs.
// Quién pregunta a cada fuente y en qué orden está en icons_artist.go
// (findArtistPic).

// ---- Metal Archives: el logo de la banda -----------------------------------

// Metal Archives es la enciclopedia del metal, y tiene el logo de casi
// cualquier grupo: justo los que no están ni en MusicBrainz ni en
// TheAudioDB, que es donde se queda corta la búsqueda por MBID.
//
// Peticiones de una en una y espaciadas un segundo, como con MusicBrainz. Y
// si no contesta —hay quien lo tiene bloqueado por su operador, y el sitio
// se cae a ratos— se deja de intentar en toda la sesión: si no, cada
// carpeta sin logo se comería el tiempo de espera.
var (
	maBase  = "https://www.metal-archives.com" // lo cambian los tests
	maDelay = time.Second                      // espera entre peticiones
)

var metalArchives = struct {
	mu     sync.Mutex
	last   time.Time
	fails  int  // fallos seguidos: con maMaxFails se deja de intentar
	down   bool // ha fallado alguna vez en esta sesión
	warned bool // ya se ha anotado en el registro
	client *http.Client
}{client: &http.Client{Timeout: 8 * time.Second}}

// maUnreachable: en esta sesión Metal Archives no ha llegado a contestar.
// Los artistas que se queden sin logo por eso se marcan como provisionales
// (ver artistSig) para volver a intentarlo cuando el sitio vuelva.
func maUnreachable() bool {
	metalArchives.mu.Lock()
	defer metalArchives.mu.Unlock()
	return metalArchives.down
}

// maDownOnce lo anota en el registro una sola vez por sesión: con 200
// carpetas, repetirlo en cada una no informa de nada.
func (a *App) maDownOnce() {
	metalArchives.mu.Lock()
	warned := metalArchives.warned
	metalArchives.warned = true
	metalArchives.mu.Unlock()
	if !warned {
		a.libLog("aviso: Metal Archives no contesta (lo bloquean algunos operadores, y el sitio se cae a ratos): esta vez, sin sus logos. Las carpetas afectadas se vuelven a intentar en el próximo lote")
	}
}

// maMaxFails: fallos de conexión seguidos tras los que se da por inalcanzable.
const maMaxFails = 2

var errMetalArchivesDown = fmt.Errorf("Metal Archives no responde")

func maGet(ctx context.Context, u string) ([]byte, error) {
	metalArchives.mu.Lock()
	if metalArchives.fails >= maMaxFails {
		metalArchives.mu.Unlock()
		return nil, errMetalArchivesDown
	}
	if wait := maDelay - time.Since(metalArchives.last); wait > 0 {
		time.Sleep(wait)
	}
	metalArchives.last = time.Now()
	metalArchives.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+userAgent)
	resp, err := metalArchives.client.Do(req)
	if err != nil {
		if ctx.Err() == nil { // cancelar el lote no cuenta como fallo suyo
			metalArchives.mu.Lock()
			metalArchives.fails++
			metalArchives.down = true
			metalArchives.mu.Unlock()
		}
		return nil, err
	}
	defer resp.Body.Close()
	metalArchives.mu.Lock()
	metalArchives.fails = 0 // contesta: un 404 no es estar caído
	metalArchives.mu.Unlock()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

var (
	// La búsqueda devuelve cada resultado como el <a> de la banda.
	maBandRe = regexp.MustCompile(`href="([^"]+/bands/[^"]+)"[^>]*>([^<]+)<`)
	// En la ficha, el enlace del logo a tamaño completo.
	maLogoRe = regexp.MustCompile(`id="logo"[^>]*href="([^"]+)"`)
)

// metalArchivesLogo: la URL del logo de la banda. Se exige que el nombre
// coincida exactamente (nada de parecidos: "Frost" son veinte bandas).
func metalArchivesLogo(ctx context.Context, name string) (string, error) {
	data, err := maGet(ctx, maBase+"/search/ajax-band-search/?field=name&query="+url.QueryEscape(name))
	if err != nil {
		return "", err
	}
	var raw struct {
		AaData [][]string `json:"aaData"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	band, ok := maBandURL(raw.AaData, name)
	if !ok {
		return "", fmt.Errorf("no está en Metal Archives")
	}
	if !strings.HasPrefix(band, maBase+"/") { // solo se siguen enlaces del propio sitio
		return "", fmt.Errorf("enlace de banda inesperado: %s", band)
	}
	page, err := maGet(ctx, band)
	if err != nil {
		return "", err
	}
	logo := maLogoURL(page)
	if logo == "" {
		return "", fmt.Errorf("su ficha no tiene logo")
	}
	return logo, nil
}

// maBandURL: la ficha de la banda que se llama exactamente así. Cada fila
// de la búsqueda trae el <a> del grupo y, a veces, sus alias detrás.
func maBandURL(rows [][]string, name string) (string, bool) {
	want := normalizeName(name)
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		m := maBandRe.FindStringSubmatch(row[0])
		if m != nil && normalizeName(html.UnescapeString(m[2])) == want {
			return html.UnescapeString(m[1]), true
		}
	}
	return "", false
}

// maLogoURL: el enlace al logo a tamaño completo de una ficha de banda.
func maLogoURL(page []byte) string {
	m := maLogoRe.FindSubmatch(page)
	if m == nil {
		return ""
	}
	return html.UnescapeString(string(m[1]))
}

// ---- las demás fuentes: TheAudioDB, Deezer, Bandcamp, Discogs --------------

// audioDBArtist: logo y foto de TheAudioDB por MBID (clave pública de
// pruebas; sin ella no hay búsqueda por nombre, que tampoco queremos).
func audioDBArtist(ctx context.Context, mbid string) (logo, thumb string, err error) {
	var raw struct {
		Artists []struct {
			Logo  string `json:"strArtistLogo"`
			Thumb string `json:"strArtistThumb"`
		} `json:"artists"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://www.theaudiodb.com/api/v1/json/2/artist-mb.php?i="+url.QueryEscape(mbid), nil, nil, &raw); err != nil {
		return "", "", err
	}
	if len(raw.Artists) == 0 {
		return "", "", fmt.Errorf("no está en TheAudioDB")
	}
	return raw.Artists[0].Logo, raw.Artists[0].Thumb, nil
}

// deezerArtistPic: busca el disco; si Deezer lo tiene de ese artista, la
// foto es la de su ficha de artista.
func deezerArtistPic(ctx context.Context, artist, album string) (string, error) {
	var raw struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name      string `json:"name"`
				PictureXL string `json:"picture_xl"`
			} `json:"artist"`
		} `json:"data"`
	}
	q := url.QueryEscape(strings.TrimSpace(artist + " " + album))
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/search/album?limit=5&q="+q, nil, nil, &raw); err != nil {
		return "", err
	}
	for _, d := range raw.Data {
		if similarity(d.Title, album) >= 0.8 && similarity(d.Artist.Name, artist) >= 0.8 && d.Artist.PictureXL != "" &&
			!strings.Contains(d.Artist.PictureXL, "/artist//") { // sin foto: Deezer devuelve una URL vacía por dentro
			return d.Artist.PictureXL, nil
		}
	}
	return "", fmt.Errorf("no está en Deezer")
}

var bandPhotoRe = regexp.MustCompile(`<img[^>]+class="band-photo"[^>]+src="([^"]+)"`)

// bandcampArtistPic: busca el disco; la página del grupo lleva su foto.
func bandcampArtistPic(ctx context.Context, artist, album string) (string, error) {
	rels, err := searchBandcamp(ctx, artist, album)
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if similarity(r.Title, album) < 0.8 || similarity(r.Artist, artist) < 0.8 {
			continue
		}
		_, albumURL := parseKey(r.ID)
		u, err := url.Parse(albumURL)
		if err != nil {
			continue
		}
		page, err := fetchBytes(ctx, u.Scheme+"://"+u.Host+"/")
		if err != nil {
			continue
		}
		if m := bandPhotoRe.FindSubmatch(page); m != nil {
			return bigBandcampImage(string(m[1])), nil
		}
	}
	return "", fmt.Errorf("no está en Bandcamp")
}

// fetchBytes descarga una URL (imagen o página) con un tope de tamaño.
func fetchBytes(ctx context.Context, u string) ([]byte, error) {
	if strings.TrimSpace(u) == "" {
		return nil, fmt.Errorf("sin URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+userAgent)
	resp, err := webClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil || len(data) < 100 {
		return nil, fmt.Errorf("respuesta vacía")
	}
	return data, nil
}

// discogsArtistPic: busca el disco en Discogs (con token), coge su primer
// artista acreditado y la imagen principal de la ficha del artista. Discogs
// tiene foto de casi cualquier grupo con una demo publicada.
func discogsArtistPic(ctx context.Context, artist, album, token string) (string, error) {
	rels, err := searchDiscogs(ctx, artist, album, token)
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if similarity(r.Title, album) < 0.8 || similarity(firstArtist(r.Artist), artist) < 0.8 {
			continue
		}
		_, id := parseKey(r.ID)
		var rel struct {
			Artists []struct {
				ID int64 `json:"id"`
			} `json:"artists"`
		}
		if err := getJSON(ctx, http.MethodGet, "https://api.discogs.com/releases/"+id+"?token="+url.QueryEscape(token), nil, nil, &rel); err != nil || len(rel.Artists) == 0 {
			continue
		}
		var art struct {
			Images []struct {
				URI  string `json:"uri"`
				Type string `json:"type"`
			} `json:"images"`
		}
		u := fmt.Sprintf("https://api.discogs.com/artists/%d?token=%s", rel.Artists[0].ID, url.QueryEscape(token))
		if err := getJSON(ctx, http.MethodGet, u, nil, nil, &art); err != nil {
			continue
		}
		for _, img := range art.Images {
			if img.Type == "primary" && img.URI != "" {
				return img.URI, nil
			}
		}
		if len(art.Images) > 0 && art.Images[0].URI != "" {
			return art.Images[0].URI, nil
		}
	}
	return "", fmt.Errorf("no está en Discogs")
}
