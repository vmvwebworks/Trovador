package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Renombrar en lote las carpetas de disco de una biblioteca al nombre que
// les toca, el mismo que pone el renombrado por edición: dentro de la
// carpeta de su artista, el título del disco a secas; suelta, "Artista -
// Álbum (Año)". Primero se propone el cambio de cada una (plan) y no se
// toca nada hasta que se confirma, porque renombrar a ciegas media
// colección es justo lo que no se puede deshacer.

// renameItem es la propuesta para una carpeta.
type renameItem struct {
	Dir    string `json:"dir"`    // carpeta del disco
	Name   string `json:"name"`   // cómo se llama ahora
	Target string `json:"target"` // cómo se llamaría (solo el nombre, no la ruta)
	Source string `json:"source"` // de dónde sale: edición | etiquetas | nombre de la carpeta
	Skip   string `json:"skip"`   // motivo por el que se queda igual; "" = se renombra
	Warn   string `json:"warn"`   // propuesta rara: se renombra igual, pero conviene mirarla
}

// albumName propone el nombre de la carpeta de un disco: la edición elegida
// en el análisis si la hay, y si no, lo que digan las etiquetas (con el
// nombre de la carpeta como último recurso, igual que en la cuadrícula).
// short dice que el nombre se ha quedado solo con el título porque la
// carpeta madre ya es la del artista.
func (a *App) albumName(dir string) (name, source string, short bool) {
	parent := filepath.Dir(dir)
	named := func(rel mbRelease, src string) (string, string, bool) {
		in := a.albumFolderIn(parent, rel)
		return in, src, in != albumFolderName(rel)
	}
	if an := a.batch.get(dir); an != nil && an.detail != nil {
		return named(an.detail.Release, "edición")
	}
	card, err := a.AlbumCard(dir)
	if err != nil {
		return "", "", false
	}
	// Las etiquetas mandan sobre el nombre de la carpeta: en un disco que ya
	// se etiquetó son las de la edición, y en uno recién bajado suelen ser
	// más limpias que "..._-_Vinyl-2013-EMS".
	artist := firstNonEmpty(card.TagArtist, card.Artist)
	title := firstNonEmpty(card.TagAlbum, card.Album)
	if strings.TrimSpace(title) == "" {
		return "", "", false
	}
	source = "etiquetas"
	if card.TagAlbum == "" {
		source = "nombre de la carpeta"
	}
	return named(mbRelease{Artist: artist, Title: title, Date: card.Date}, source)
}

// RenamePlan propone el nombre de cada carpeta. No renombra nada.
func (a *App) RenamePlan(dirs []string) ([]renameItem, error) {
	items := make([]renameItem, 0, len(dirs))
	planned := map[string]bool{} // nombres ya pedidos en este plan
	for _, dir := range dirs {
		if a.ctx.Err() != nil {
			return nil, a.ctx.Err()
		}
		dir = filepath.Clean(dir)
		if !fileExists(dir) { // renombrada o movida mientras tanto
			continue
		}
		it := renameItem{Dir: dir, Name: filepath.Base(dir)}
		var short bool
		it.Target, it.Source, short = a.albumName(dir)
		switch {
		case it.Target == "":
			it.Skip = "no sé cómo se llama (sin etiquetas ni edición: analiza o etiqueta el disco)"
		case strings.EqualFold(it.Target, it.Name):
			it.Skip = "ya se llama así"
		case fileExists(filepath.Join(filepath.Dir(dir), it.Target)):
			it.Skip = "ya existe una carpeta «" + it.Target + "» al lado (mira si es un duplicado)"
		case planned[targetKey(dir, it.Target)]:
			it.Skip = "otro disco de este plan se va a llamar así (mira si es un duplicado)"
		}
		if it.Skip == "" {
			planned[targetKey(dir, it.Target)] = true
			// Quitar "FLAC" o un año repetido es lo que se busca; perder una
			// palabra del título no. Pasa en splits y recopilaciones, donde
			// las etiquetas son de otro disco: se propone igual, pero
			// diciendo qué se deja por el camino. El artista que se quita
			// porque lo dice la carpeta madre no cuenta: sigue en la ruta.
			context := it.Target
			if short {
				context += " " + filepath.Base(filepath.Dir(dir))
			}
			if lost := lostWords(it.Name, context); len(lost) > 0 {
				it.Warn = "se pierde «" + strings.Join(lost, " ") + "»"
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// Palabras cuya pérdida no dice nada: el formato, la calidad y las
// coletillas que traen las carpetas descargadas. Son justo las que se
// quieren quitar.
var renameNoise = map[string]bool{
	"flac": true, "mp3": true, "wav": true, "ogg": true, "opus": true, "aac": true,
	"web": true, "cdr": true, "cda": true, "vinyl": true, "vinilo": true, "tape": true,
	"kbps": true, "kbit": true, "320": true, "256": true, "192": true, "128": true,
	"full": true, "album": true, "reissue": true, "remaster": true, "remastered": true,
	"limited": true, "edition": true, "digipak": true, "rip": true, "scene": true,
}

// lostWords: palabras del nombre de ahora que el nuevo no conserva, sin
// contar las que dan igual (formato, calidad, año) ni las muy cortas.
func lostWords(old, neu string) []string {
	have := map[string]bool{}
	for _, w := range strings.Fields(normalizeName(neu)) {
		have[w] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(normalizeName(old)) {
		if have[w] || seen[w] || len(w) < 4 || renameNoise[w] || yearRe.MatchString(w) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// targetKey identifica un nombre dentro de su carpeta madre, para detectar dos
// discos del plan que acabarían llamándose igual en el mismo sitio.
func targetKey(dir, name string) string {
	return strings.ToLower(filepath.Join(filepath.Dir(dir), name))
}

// RenameApply renombra las carpetas del plan que se le pasen. Cada una se
// comprueba otra vez al vuelo: entre el plan y la confirmación puede haber
// cambiado algo. Devuelve cuántas renombró.
func (a *App) RenameApply(items []renameItem) (int, error) {
	done := 0
	for _, it := range items {
		if a.ctx.Err() != nil {
			return done, a.ctx.Err()
		}
		if it.Skip != "" || it.Target == "" || it.Dir == "" {
			continue
		}
		if !fileExists(it.Dir) {
			a.libLog("%s: ya no está esa carpeta", it.Name)
			continue
		}
		if _, err := a.renameAlbumTo(it.Dir, it.Target); err != nil {
			a.libLog("%s: %v", it.Name, err)
			continue
		}
		done++
	}
	a.libLog("Renombrar: %d carpeta(s) renombradas de %d", done, len(items))
	if done == 0 && len(items) > 0 {
		return 0, fmt.Errorf("no se pudo renombrar ninguna; mira el registro")
	}
	return done, nil
}
