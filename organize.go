package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// Separadores que solo aparecen entre artistas distintos. "&" y "," no
// valen: están dentro de nombres de grupo (Simon & Garfunkel; Earth, Wind &
// Fire), así que solo se parten cuando la fuente ya da los artistas sueltos.
var artistSplit = regexp.MustCompile(`(?i)\s*(/|;|\|| feat\.? | ft\.? | featuring | vs\.? | with )\s*`)

// firstArtist: el primer artista de un crédito conjunto.
func firstArtist(credit string) string {
	parts := artistSplit.Split(strings.TrimSpace(credit), 2)
	return strings.TrimSpace(parts[0])
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

// artistKey normaliza para comparar nombres de carpeta de artista:
// "The Cure" y "Cure, The" son la misma.
func artistKey(name string) string {
	k := normalizeName(name)
	k = strings.TrimSuffix(k, " the")
	k = strings.TrimPrefix(k, "the ")
	return strings.TrimSpace(k)
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

// insideOf: p está dentro de dir (o es dir).
func insideOf(p, dir string) bool {
	p, dir = strings.ToLower(filepath.Clean(p)), strings.ToLower(filepath.Clean(dir))
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
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
		case strings.EqualFold(it.Target, dir):
			it.Skip = "ya está donde toca"
		case fileExists(it.Target) || planned[strings.ToLower(it.Target)]:
			it.Skip = "ya hay una carpeta con ese nombre en " + it.Artist + " (mira si es un duplicado)"
		case strings.EqualFold(it.ArtistDir, dir):
			it.Skip = "la carpeta del artista se llamaría igual que este disco; renómbralo (p. ej. «" + it.Artist + " - " + it.Name + " (año)») y vuelve a probar"
		case insideOf(it.ArtistDir, dir) || insideOf(collection, dir):
			it.Skip = "el destino estaría dentro del propio disco"
		}
		if it.Skip == "" {
			planned[strings.ToLower(it.Target)] = true
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
		fixIconAttrs(it.Target) // por si fue copia y no renombrado
		a.batch.move(it.Dir, it.Target)
		favMove(it.Dir, it.Target)
		a.emit("album-moved", map[string]string{"from": it.Dir, "to": it.Target})
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

// moveDir mueve una carpeta; si está en otra unidad (rename no vale), la
// copia fichero a fichero y borra el origen.
func moveDir(ctx context.Context, from, to string) error {
	if insideOf(to, from) {
		return fmt.Errorf("el destino %s está dentro del origen", to)
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	// Otra unidad, o un fichero abierto: copia y borrado.
	if err := copyTree(ctx, from, to); err != nil {
		os.RemoveAll(to)
		return fmt.Errorf("no pude mover a %s: %v", to, err)
	}
	return os.RemoveAll(from)
}

func copyTree(ctx context.Context, from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(from, path)
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}
