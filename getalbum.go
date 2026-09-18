package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Traerse un disco entero desde un resultado de búsqueda: se crea su
// carpeta en la de descargas (Artista - Álbum (Año)) con la carátula, y el
// panel de Completar busca cada pista en Soulseek, Bandcamp y YouTube.

// CreateAlbum crea (o reutiliza) la carpeta del disco en la carpeta de
// descargas, baja la carátula y devuelve qué pistas faltan (todas, si es
// nueva).
func (a *App) CreateAlbum(key string) (buildResult, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return buildResult{}, err
	}
	if len(rel.Tracks) == 0 {
		return buildResult{}, fmt.Errorf("la edición no tiene pistas")
	}
	a.mu.Lock()
	out := a.settings.OutDir
	a.mu.Unlock()
	// La carpeta de descargas es plana por definición: aquí el nombre va
	// entero aunque esa carpeta cuelgue de la biblioteca que se esté mirando.
	dir := filepath.Join(out, albumFolderName(rel.Release))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return buildResult{}, err
	}
	a.libLog("álbum: %s", dir)
	cover := filepath.Join(dir, "cover.jpg")
	if !fileExists(cover) {
		if src, err := coverFor(a.ctx, rel, cover, a.searchOpts(false)); err != nil {
			a.libLog("aviso: carátula: %v", err)
		} else {
			a.libLog("carátula de %s", src)
		}
	}
	res := buildResult{Dir: dir}
	res.Missing, err = a.missingTracks(dir, rel)
	a.autoIcon(dir, rel.Release.Format)
	return res, err
}

// FindAlbumSourcesBandcamp busca el disco en Bandcamp y, si está, devuelve
// para cada pista que falta la URL de esa misma pista allí (yt-dlp la baja
// igual que un vídeo). Es la fuente más fiable cuando existe: tracklist
// exacta y audio del propio grupo.
func (a *App) FindAlbumSourcesBandcamp(key, artist, album string, missing []mbTrack) (map[int][]trackSource, error) {
	ctx := a.ctx
	var det mbReleaseDetail
	if src, _ := parseKey(key); src == srcBandcamp {
		d, err := a.GetRelease(key) // la edición ya es de Bandcamp: sus propias pistas
		if err != nil {
			return nil, err
		}
		det = d
	} else {
		rels, err := searchBandcamp(ctx, artist, stripParen(album))
		if err != nil {
			return nil, err
		}
		found := false
		for _, r := range rels {
			if similarity(r.Title, album) < 0.8 || (artist != "" && similarity(r.Artist, artist) < 0.7) {
				continue
			}
			d, err := a.GetRelease(r.ID)
			if err != nil {
				continue
			}
			det, found = d, true
			break
		}
		if !found {
			return map[int][]trackSource{}, nil
		}
	}
	out := map[int][]trackSource{}
	for _, m := range missing {
		for _, t := range det.Tracks {
			if t.URL == "" {
				continue
			}
			sim := similarity(t.Title, m.Title)
			diff := t.Length - m.Length
			if sim < 0.6 && (m.Length == 0 || diff < -3 || diff > 3) {
				continue
			}
			out[m.Number] = append(out[m.Number], trackSource{
				Source: "bandcamp", URL: t.URL, Title: t.Title, Channel: det.Release.Artist,
				Duration: t.Length, Diff: diff, Sim: sim, Score: 0.9 + 0.1*sim,
			})
		}
	}
	return out, nil
}

// bandcampTrackURL: la URL absoluta de una pista a partir del enlace
// relativo de la página del álbum.
func bandcampTrackURL(albumURL, link string) string {
	if link == "" {
		return ""
	}
	u, err := url.Parse(albumURL)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(link, "http") {
		return link
	}
	return u.Scheme + "://" + u.Host + link
}
