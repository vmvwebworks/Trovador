package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Ordenar la colección por artista: cada disco va dentro de la carpeta de
// su artista (Colección\Artista\Disco). Si la carpeta del artista no
// existe, se crea. En un disco entre varios artistas manda el primero que
// aparece acreditado. Primero se propone (plan) y luego se mueve.

// organizeItem es la propuesta para un disco.
type organizeItem struct {
	Dir       string `json:"dir"`       // carpeta del disco
	Name      string `json:"name"`      // su nombre
	Artist    string `json:"artist"`    // artista elegido (nombre de carpeta)
	Source    string `json:"source"`    // de dónde sale: edición | etiquetas | nombre
	ArtistDir string `json:"artistDir"` // carpeta del artista (existente o por crear)
	NewArtist bool   `json:"newArtist"` // hay que crearla
	Target    string `json:"target"`    // destino final del disco
	Skip      string `json:"skip"`      // motivo por el que no se mueve; "" = se mueve
}

// albumArtist decide el artista de una carpeta: la edición elegida en el
// análisis (que trae los acreditados por separado), y si no, lo que dicen
// las etiquetas y el nombre de la carpeta.
func (a *App) albumArtist(dir string) (artist, source string) {
	if an := a.batch.get(dir); an != nil && an.detail != nil {
		r := an.detail.Release
		if r.FirstArtist != "" {
			return r.FirstArtist, "edición"
		}
		if r.Artist != "" {
			return firstArtist(r.Artist), "edición"
		}
	}
	if card, err := a.AlbumCard(dir); err == nil && card.Artist != "" {
		return firstArtist(card.Artist), "etiquetas o nombre"
	}
	return "", ""
}

// findArtistDir busca en la colección una carpeta que sea ese artista. Una
// carpeta con audio dentro es un disco, no un artista (un disco homónimo
// —"Lost Castles/Lost Castles"— no puede hacer de carpeta del artista, y
// mucho menos meterse dentro de sí mismo).
func findArtistDir(collection, artist string) string {
	entries, err := os.ReadDir(collection)
	if err != nil {
		return ""
	}
	want := artistKey(artist)
	for _, e := range entries {
		if !e.IsDir() || artistKey(e.Name()) != want {
			continue
		}
		if p := filepath.Join(collection, e.Name()); countAudio(p) == 0 {
			return p
		}
	}
	return ""
}

// OrganizePlan propone dónde iría cada disco dentro de la colección. No
// mueve nada.
func (a *App) OrganizePlan(dirs []string, collection string) ([]organizeItem, error) {
	collection = filepath.Clean(collection)
	if info, err := os.Stat(collection); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("la carpeta de la colección no existe: %s", collection)
	}
	items := make([]organizeItem, 0, len(dirs))
	planned := map[string]bool{} // destinos ya asignados en este plan
	for _, dir := range dirs {
		if a.ctx.Err() != nil {
			return nil, a.ctx.Err()
		}
		dir = filepath.Clean(dir)
		it := organizeItem{Dir: dir, Name: filepath.Base(dir)}
		it.Artist, it.Source = a.albumArtist(dir)
		if it.Artist == "" {
			it.Skip = "no sé de qué artista es (elige una edición o etiqueta el disco)"
			items = append(items, it)
			continue
		}
		it.Artist = safeName(it.Artist)
		if checkFolderName(it.Artist) != nil {
			it.Skip = "el nombre del artista no sirve como carpeta"
			items = append(items, it)
			continue
		}
		if artistKey(filepath.Base(filepath.Dir(dir))) == artistKey(it.Artist) {
			it.ArtistDir = filepath.Dir(dir)
			it.Skip = "ya está en la carpeta de " + filepath.Base(it.ArtistDir)
			items = append(items, it)
			continue
		}
		if it.ArtistDir = findArtistDir(collection, it.Artist); it.ArtistDir == "" {
			it.ArtistDir = filepath.Join(collection, it.Artist)
			it.NewArtist = true
		} else {
			it.Artist = filepath.Base(it.ArtistDir) // la carpeta existente manda en la grafía
		}
		it.Target = filepath.Join(it.ArtistDir, it.Name)
		switch {
		case samePath(it.Target, dir):
			it.Skip = "ya está donde toca"
		case fileExists(it.Target) || planned[pathKey(it.Target)]:
			it.Skip = "ya hay una carpeta con ese nombre en " + it.Artist + " (mira si es un duplicado)"
		case samePath(it.ArtistDir, dir):
			it.Skip = "la carpeta del artista se llamaría igual que este disco; renómbralo (p. ej. «" + it.Artist + " - " + it.Name + " (año)») y vuelve a probar"
		case insideOf(it.ArtistDir, dir) || insideOf(collection, dir):
			it.Skip = "el destino estaría dentro del propio disco"
		}
		if it.Skip == "" {
			planned[pathKey(it.Target)] = true
		}
		items = append(items, it)
	}
	return items, nil
}

// OrganizeApply mueve los discos del plan que no están omitidos, creando
// las carpetas de artista que hagan falta. Devuelve cuántos movió.
func (a *App) OrganizeApply(items []organizeItem) (int, error) {
	moved := 0
	touched := map[string]bool{} // carpetas de artista con discos nuevos
	for _, it := range items {
		if a.ctx.Err() != nil {
			return moved, a.ctx.Err()
		}
		if it.Skip != "" || it.Target == "" {
			continue
		}
		if fileExists(it.Target) {
			a.libLog("%s: ya existe %s, no se mueve", it.Name, it.Target)
			continue
		}
		if insideOf(it.Target, it.Dir) || countAudio(it.ArtistDir) > 0 {
			a.libLog("%s: destino %s no válido, no se mueve", it.Name, it.Target)
			continue
		}
		if err := os.MkdirAll(it.ArtistDir, 0o755); err != nil {
			a.libLog("%s: no pude crear %s: %v", it.Name, it.ArtistDir, err)
			continue
		}
		if err := moveDir(a.ctx, it.Dir, it.Target); err != nil {
			a.libLog("%s: %v", it.Name, err)
			continue
		}
		a.moved(it.Dir, it.Target) // también si fue copia y no renombrado
		a.libLog("%s  ->  %s", it.Name, it.Target)
		touched[it.ArtistDir] = true
		moved++
	}
	a.libLog("Ordenar por artista: %d disco(s) movidos", moved)
	// Las carpetas de artista tocadas reciben (o actualizan) su icono.
	a.mu.Lock()
	icons := a.settings.FolderIcons
	a.mu.Unlock()
	if icons && moved > 0 {
		for dir := range touched {
			if _, err := a.refreshArtistIcon(a.ctx, dir); err != nil {
				a.libLog("aviso: icono de artista %s: %v", filepath.Base(dir), err)
			}
		}
	}
	return moved, nil
}
