package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Encuadre manual de la carátula en la cartulina de la caja de casete (la
// pletina): la parte de la imagen que se ve, en fracciones 0-1 del ancho y
// del alto. La detección automática del lomo acierta casi siempre, pero
// no siempre; con esto se corrige a mano y queda guardado en
// %APPDATA%\Trovador\crops.json. Sigue a la carpeta si se mueve.

type coverCrop struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type cropStore struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]coverCrop
}

var crops cropStore

func cropPath() string { return filepath.Join(cacheDir(), "crops.json") }

func (c *cropStore) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.m = map[string]coverCrop{}
	if data, err := os.ReadFile(cropPath()); err == nil {
		_ = json.Unmarshal(data, &c.m)
	}
}

func (c *cropStore) save() error {
	data, err := json.MarshalIndent(c.m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cropPath(), data, 0o644)
}

func (c *cropStore) key(dir string) string {
	dir = filepath.Clean(dir)
	for k := range c.m {
		if strings.EqualFold(k, dir) {
			return k
		}
	}
	return dir
}

// CoverCrops: todos los encuadres guardados, por carpeta.
func (a *App) CoverCrops() (map[string]coverCrop, error) {
	crops.mu.Lock()
	defer crops.mu.Unlock()
	crops.load()
	out := make(map[string]coverCrop, len(crops.m))
	for k, v := range crops.m {
		out[k] = v
	}
	return out, nil
}

// SetCoverCrop guarda el encuadre de un disco; con on=false lo quita (vuelve
// al automático).
func (a *App) SetCoverCrop(dir string, on bool, c coverCrop) error {
	crops.mu.Lock()
	defer crops.mu.Unlock()
	crops.load()
	k := crops.key(dir)
	if on {
		crops.m[k] = c
	} else {
		delete(crops.m, k)
	}
	return crops.save()
}

// cropMove: la carpeta de un disco ha cambiado de ruta.
func cropMove(from, to string) {
	crops.mu.Lock()
	defer crops.mu.Unlock()
	crops.load()
	k := crops.key(from)
	if c, ok := crops.m[k]; ok {
		delete(crops.m, k)
		crops.m[filepath.Clean(to)] = c
		_ = crops.save()
	}
}
