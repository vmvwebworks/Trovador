package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Revisión de lo que propone el análisis: el usuario ve las candidatas,
// acepta la propuesta o elige otra (incluso pegando una URL de MusicBrainz
// encontrada en Google/Discogs), y solo lo confirmado se aplica.

// ChooseCandidate fija una edición concreta para un disco (de las candidatas
// o cualquier ID/URL de MusicBrainz), la evalúa y la da por confirmada.
func (a *App) ChooseCandidate(dir, releaseID string) (albumInfo, error) {
	an := a.batch.get(dir)
	if an == nil {
		return albumInfo{}, fmt.Errorf("primero analiza la carpeta")
	}
	// Acepta una clave de candidata ("deezer:123", "bandcamp:https://..."),
	// un ID de MusicBrainz, o una URL de MusicBrainz pegada.
	if source, _ := parseKey(releaseID); source == srcMusicBrainz {
		if id := extractMBID(releaseID); id != "" {
			releaseID = id
		} else {
			return an.info, fmt.Errorf("no reconozco ese identificador")
		}
	}

	d, ok := an.details[releaseID]
	if !ok {
		got, err := a.GetRelease(releaseID)
		if err != nil {
			return an.info, err
		}
		d = &got
		if an.details == nil {
			an.details = make(map[string]*mbReleaseDetail)
		}
		an.details[releaseID] = d
	}
	state, matched, score := evaluate(d, an.scan)

	// Reflejarlo en la lista de candidatas (o añadirla si vino de fuera).
	found := false
	for i := range an.info.Candidates {
		if c := &an.info.Candidates[i]; c.Release.ID == releaseID {
			c.Evaluated, c.State, c.Matched, c.Score = true, state, matched, score
			found = true
		}
	}
	if !found {
		an.info.Candidates = append(an.info.Candidates,
			candidateInfo{Release: d.Release, Evaluated: true, State: state, Matched: matched, Score: score})
	}
	sortCandidates(an.info.Candidates)

	an.detail = d
	rel := d.Release
	an.info.Release = &rel
	an.info.Matched = matched
	an.info.Confirmed = true
	a.setAlbum(an, state, describe(state, matched, len(an.scan.Files), d))
	a.renameIfFits(an)
	return an.info, nil
}

// renameIfFits: elegir una edición que cuadra es decidir cómo se llama el
// disco, así que la carpeta se renombra ya, sin esperar a aplicar nada.
func (a *App) renameIfFits(an *albumAnalysis) {
	if an.detail == nil || !fits(an.info.State) {
		return
	}
	if _, err := a.renameToEdition(an.info.Dir, an.detail.Release); err != nil {
		a.libLog("%s: %v", an.info.Name, err)
	}
	a.emit("album", an.info)
}

// ConfirmAlbum acepta la edición propuesta para un disco.
func (a *App) ConfirmAlbum(dir string) (albumInfo, error) {
	an := a.batch.get(dir)
	if an == nil || an.detail == nil {
		return albumInfo{}, fmt.Errorf("este disco no tiene edición propuesta")
	}
	an.info.Confirmed = true
	a.emit("album", an.info)
	a.renameIfFits(an)
	return an.info, nil
}

// ConfirmMatching acepta de golpe las propuestas que cuadran (íntegras o
// separables) de las carpetas indicadas. Devuelve cuántas.
func (a *App) ConfirmMatching(dirs []string) int {
	n := 0
	for _, dir := range dirs {
		an := a.batch.get(dir)
		if an == nil || an.detail == nil || an.info.Confirmed {
			continue
		}
		if identified(an.info.State) {
			an.info.Confirmed = true
			n++
			a.emit("album", an.info)
			a.renameIfFits(an)
		}
	}
	return n
}

// OpenSearch abre en el navegador una búsqueda del texto en el sitio pedido.
// Para lo que MusicBrainz no cubre: mirar en Discogs, Bandcamp o Google y
// volver con la edición correcta (o su URL de MusicBrainz).
func (a *App) OpenSearch(site, query string) error {
	q := url.QueryEscape(strings.TrimSpace(query))
	var u string
	switch site {
	case "google":
		u = "https://www.google.com/search?q=" + q
	case "discogs":
		u = "https://www.discogs.com/search/?type=release&q=" + q
	case "bandcamp":
		u = "https://bandcamp.com/search?item_type=a&q=" + q
	case "musicbrainz":
		u = "https://musicbrainz.org/search?type=release&method=indexed&query=" + q
	default:
		return fmt.Errorf("sitio desconocido: %s", site)
	}
	runtime.BrowserOpenURL(a.ctx, u)
	return nil
}

var mbidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// extractMBID saca el identificador de una URL de MusicBrainz
// (https://musicbrainz.org/release/<id>) o de un texto que lo contenga.
func extractMBID(s string) string {
	return mbidRe.FindString(s)
}
