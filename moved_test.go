package main

import (
	"path/filepath"
	"slices"
	"testing"
)

// storedFavs: la lista guardada tal cual (Favorites solo enseña las
// carpetas que tienen audio).
func storedFavs() []string {
	favs.mu.Lock()
	defer favs.mu.Unlock()
	favs.load()
	return slices.Clone(favs.dirs)
}

// Al mover un disco le siguen su favorito y su encuadre, y la ventana se
// entera.
func TestMoved(t *testing.T) {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	favs, crops = favStore{}, cropStore{}
	var events []string
	app := &App{emit: func(event string, data ...any) { events = append(events, event) }}
	root := t.TempDir()
	from, to := filepath.Join(root, "Disco"), filepath.Join(root, "Artista", "Disco (1996)")
	c := coverCrop{X: 0.1, Y: 0, W: 0.9, H: 1}
	app.SetFavorite(from, true)
	app.SetCoverCrop(from, true, c)

	app.moved(from, to)
	if l := storedFavs(); len(l) != 1 || l[0] != to {
		t.Errorf("favorito: %v", l)
	}
	if m, _ := app.CoverCrops(); len(m) != 1 || m[to] != c {
		t.Errorf("encuadre: %v", m)
	}
	if len(events) != 1 || events[0] != "album-moved" {
		t.Errorf("eventos: %v", events)
	}
}

// Al fusionar duplicados el favorito pasa a la copia que se queda; si esa ya
// era favorita, queda una sola vez.
func TestFavMoveMerges(t *testing.T) {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	favs = favStore{}
	app := &App{}
	root := t.TempDir()
	keep, a, b := filepath.Join(root, "buena"), filepath.Join(root, "copia1"), filepath.Join(root, "copia2")
	app.SetFavorite(a, true)
	favMove(a, keep)
	if l := storedFavs(); len(l) != 1 || l[0] != keep {
		t.Fatalf("pasa a la buena: %v", l)
	}
	app.SetFavorite(b, true)
	favMove(b, keep)
	if l := storedFavs(); len(l) != 1 || l[0] != keep {
		t.Errorf("sin repetir: %v", l)
	}
}
