package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Escuchar un disco antes de bajarlo. Cada fuente da lo que tiene:
//
//   - Bandcamp: las pistas enteras (el mismo MP3 de 128 kb/s de su web)
//   - Deezer: un fragmento de 30 s de cada pista
//   - YouTube: a petición, pista a pista, bajada por yt-dlp a una carpeta temporal
//
// Un disco de MusicBrainz o Discogs no trae audio: se busca el mismo disco
// en Bandcamp y en Deezer, y gana Bandcamp porque suena entero.
//
// El audio no lo pide la ventana directamente: pasa por /preview?u= (como
// el de los ficheros por /media), para no depender de CORS ni de cómo trate
// WebView2 las URLs de fuera.

type previewResult struct {
	Release mbRelease `json:"release"` // de dónde sale el audio (título, fuente, carátula)
	Tracks  []mbTrack `json:"tracks"`  // con Stream si se pueden escuchar
	Full    bool      `json:"full"`    // pistas enteras (Bandcamp) o fragmentos (Deezer)
	Top     bool      `json:"top"`     // muestra de un artista: sus pistas más escuchadas, no un disco
}

// ArtistSample busca algo que escuchar de un artista para hacerse una idea:
// sus pistas más escuchadas en Deezer (30 s cada una); si no está, uno de
// sus discos en Bandcamp (entero); y si tampoco, sus canciones más
// escuchadas según Last.fm, sin audio: la ventana las busca en YouTube.
func (a *App) ArtistSample(name, deezerID string) (previewResult, error) {
	ctx := a.ctx
	name = strings.TrimSpace(name)
	var (
		wg     sync.WaitGroup
		dz, bc *previewResult
	)
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
		}
		if tracks, err := deezerTop(ctx, id); err == nil && len(tracks) > 0 {
			dz = &previewResult{Release: mbRelease{Source: srcDeezer, Title: "lo más escuchado", Artist: name}, Tracks: tracks, Top: true}
		}
	}()
	go func() {
		defer wg.Done()
		rels, err := searchBandcamp(ctx, name, "")
		if err != nil {
			return
		}
		for i, r := range rels {
			if i >= 6 || normalizeName(r.Artist) != normalizeName(name) {
				continue
			}
			if d, err := getDetail(ctx, r.ID, ""); err == nil && hasStreams(d) {
				d.Release.CoverURL = firstNonEmpty(d.Release.CoverURL, r.CoverURL)
				bc = &previewResult{Release: d.Release, Tracks: d.Tracks, Full: true}
				return
			}
		}
	}()
	wg.Wait()
	switch {
	case dz != nil:
		return *dz, nil
	case bc != nil:
		return *bc, nil
	}

	a.mu.Lock()
	key := strings.TrimSpace(a.settings.LastfmKey)
	a.mu.Unlock()
	if key != "" {
		if tracks, err := lastfmTopTracks(ctx, key, name); err == nil && len(tracks) > 0 {
			return previewResult{Release: mbRelease{Source: "lastfm", Title: "lo más escuchado", Artist: name}, Tracks: tracks, Top: true}, nil
		}
	}
	return previewResult{}, fmt.Errorf("no encuentro nada de «%s» en Deezer ni en Bandcamp", name)
}

// deezerTop: las pistas más escuchadas del artista, con su fragmento.
func deezerTop(ctx context.Context, id string) ([]mbTrack, error) {
	top, err := deezerTopAlbums(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []mbTrack
	for _, t := range top {
		if t.preview == "" {
			continue
		}
		out = append(out, mbTrack{Number: len(out) + 1, Title: t.title, Artist: t.artist, Length: t.length, Stream: t.preview, Album: t.album})
	}
	return out, nil
}

// lastfmTopTracks: las canciones más escuchadas del artista según Last.fm.
func lastfmTopTracks(ctx context.Context, key, name string) ([]mbTrack, error) {
	if err := lfWait(ctx); err != nil {
		return nil, err
	}
	params := url.Values{"method": {"artist.gettoptracks"}, "api_key": {key}, "format": {"json"}, "limit": {"10"}, "autocorrect": {"1"}, "artist": {name}}
	var raw struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
		Top     struct {
			Track []struct {
				Name string `json:"name"`
			} `json:"track"`
		} `json:"toptracks"`
	}
	if err := getJSON(ctx, http.MethodGet, lastfmBase+"?"+params.Encode(), nil, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Error != 0 {
		return nil, fmt.Errorf("%s", raw.Message)
	}
	var out []mbTrack
	for _, t := range raw.Top.Track {
		out = append(out, mbTrack{Number: len(out) + 1, Title: t.Name, Artist: name})
	}
	return out, nil
}

// PreviewRelease busca audio para escuchar el disco `key`. Si no hay en
// ninguna fuente, devuelve su tracklist sin Stream: la ventana ofrece
// entonces YouTube pista a pista.
func (a *App) PreviewRelease(key, artist, title string) (previewResult, error) {
	source, _ := parseKey(key)
	if source == srcBandcamp || source == srcDeezer {
		if d, err := a.GetRelease(key); err == nil && hasStreams(d) {
			return previewResult{Release: d.Release, Tracks: d.Tracks, Full: source == srcBandcamp}, nil
		}
	}

	// El mismo disco en Bandcamp y en Deezer, a la vez.
	var (
		wg     sync.WaitGroup
		bc, dz *mbReleaseDetail
	)
	find := func(search func() ([]mbRelease, error), out **mbReleaseDetail) {
		defer wg.Done()
		rels, err := search()
		if err != nil {
			return
		}
		for _, r := range rels {
			if !sameDiscTitle(r.Title, title) || (artist != "" && similarity(r.Artist, artist) < 0.8) {
				continue
			}
			if d, err := getDetail(a.ctx, r.ID, ""); err == nil && hasStreams(d) {
				*out = &d
				return
			}
		}
	}
	short := stripParen(title)
	wg.Add(2)
	go find(func() ([]mbRelease, error) { return searchBandcamp(a.ctx, artist, short) }, &bc)
	go find(func() ([]mbRelease, error) { return searchDeezer(a.ctx, artist, short) }, &dz)
	wg.Wait()
	switch {
	case bc != nil:
		return previewResult{Release: bc.Release, Tracks: bc.Tracks, Full: true}, nil
	case dz != nil:
		return previewResult{Release: dz.Release, Tracks: dz.Tracks}, nil
	}

	d, err := a.GetRelease(key)
	if err != nil {
		return previewResult{}, err
	}
	return previewResult{Release: d.Release, Tracks: d.Tracks}, nil
}

func hasStreams(d mbReleaseDetail) bool {
	for _, t := range d.Tracks {
		if t.Stream != "" {
			return true
		}
	}
	return false
}

// PreviewYouTube baja a una carpeta temporal el audio del primer resultado
// de YouTube para la pista y devuelve su ruta (la ventana la toca por
// /media, como un fichero más). No se reenvía la URL de googlevideo: sin
// pedirla a trozos, como hace yt-dlp, YouTube la sirve a cámara lenta.
// Mejor M4A (AAC), que WebView2 reproduce seguro.
func (a *App) PreviewYouTube(artist, title string) (string, error) {
	p, _, err := a.youtubeAudio(artist, title)
	return p, err
}

// youtubeAudio hace el trabajo de PreviewYouTube y además devuelve la
// miniatura del vídeo (la portada de la radio cuando no se sabe el disco).
func (a *App) youtubeAudio(artist, title string) (path, thumb string, err error) {
	query := strings.TrimSpace(artist + " " + title)
	dir := filepath.Join(os.TempDir(), "trovador-escucha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	cleanPreviews(dir, 8)
	// Ruta y miniatura en una línea, separadas por «|» (que no puede ir en
	// una ruta de Windows).
	cmd := exec.CommandContext(a.ctx, toolPath("yt-dlp.exe"), "--no-warnings", "--no-playlist", "--no-part",
		"-f", "bestaudio[ext=m4a]/bestaudio", "--print", "after_move:%(filepath)s|%(thumbnail)s",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"), "ytsearch1:"+query)
	hideWindow(cmd)
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("YouTube: %w", err)
	}
	line := strings.TrimSpace(string(out))
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = strings.TrimSpace(line[i+1:])
	}
	path, thumb, _ = strings.Cut(line, "|")
	if thumb == "NA" {
		thumb = ""
	}
	if !fileExists(path) {
		return "", "", fmt.Errorf("YouTube no da audio para «%s»", query)
	}
	return path, thumb, nil
}

// cleanPreviews deja en la carpeta temporal solo las `keep` escuchas más
// recientes.
func cleanPreviews(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			files = append(files, f{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for i := keep; i < len(files); i++ {
		os.Remove(files[i].path)
	}
}

// previewHandler sirve /preview?u=<URL http(s) de audio>: la pide fuera y
// la reenvía tal cual, con su tipo.
func previewHandler(w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(r.URL.Query().Get("u"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		http.Error(w, "URL no válida", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+userAgent)
	resp, err := previewClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "origen: "+resp.Status, http.StatusBadGateway)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "audio/") {
		ct = "audio/mpeg" // Bandcamp y Deezer: MP3
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	if n := resp.Header.Get("Content-Length"); n != "" {
		w.Header().Set("Content-Length", n)
	}
	io.Copy(w, resp.Body)
}

// Sin el límite de 30 s de webClient: una pista entera de Bandcamp con una
// conexión lenta puede tardar más.
var previewClient = &http.Client{}
