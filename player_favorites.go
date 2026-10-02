package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Favoritos del reproductor: discos marcados con ♥, que salen en la
// gramola. Se guardan en %APPDATA%\Trovador\favorites.json como lista
// de carpetas; si una carpeta se renombra o se mueve desde la app, el
// favorito la sigue (App.moved).

type favStore struct {
	mu     sync.Mutex
	loaded bool
	dirs   []string
}

var favs favStore

func favPath() string { return filepath.Join(cacheDir(), "favorites.json") }

func (f *favStore) load() {
	if f.loaded {
		return
	}
	f.loaded = true
	if data, err := os.ReadFile(favPath()); err == nil {
		_ = json.Unmarshal(data, &f.dirs)
	}
}

func (f *favStore) save() error {
	data, err := json.MarshalIndent(f.dirs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(favPath(), data, 0o644)
}

func (f *favStore) index(dir string) int {
	dir = filepath.Clean(dir)
	for i, d := range f.dirs {
		if samePath(d, dir) {
			return i
		}
	}
	return -1
}

// Favorites: las carpetas marcadas que siguen existiendo, en orden natural.
func (a *App) Favorites() ([]string, error) {
	favs.mu.Lock()
	defer favs.mu.Unlock()
	favs.load()
	out := make([]string, 0, len(favs.dirs))
	for _, d := range favs.dirs {
		if countAudio(d) > 0 {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return naturalLess(out[i], out[j]) })
	return out, nil
}

// SetFavorite marca o desmarca un disco y devuelve la lista resultante.
func (a *App) SetFavorite(dir string, on bool) ([]string, error) {
	favs.mu.Lock()
	favs.load()
	i := favs.index(dir)
	switch {
	case on && i < 0:
		favs.dirs = append(favs.dirs, filepath.Clean(dir))
	case !on && i >= 0:
		favs.dirs = append(favs.dirs[:i], favs.dirs[i+1:]...)
	}
	err := favs.save()
	favs.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return a.Favorites()
}

// favMove: el favorito de una carpeta pasa a otra (la misma, movida; o la
// copia que se queda al fusionar duplicados). Si la otra ya era favorita,
// queda una sola vez. Lo llama App.moved.
func favMove(from, to string) {
	favs.mu.Lock()
	defer favs.mu.Unlock()
	favs.load()
	i := favs.index(from)
	if i < 0 {
		return
	}
	if favs.index(to) >= 0 {
		favs.dirs = append(favs.dirs[:i], favs.dirs[i+1:]...)
	} else {
		favs.dirs[i] = filepath.Clean(to)
	}
	_ = favs.save()
}
