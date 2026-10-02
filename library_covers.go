package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Carátulas y tarjetas de la Biblioteca y del reproductor: la carátula de
// cada disco (cover.jpg o la incrustada) y la tarjeta de la cuadrícula
// (artista, álbum, miniatura), con su caché en memoria. La caché en disco
// (cards.json y thumbs/) está en player.go.

// ---- carátulas para la vista de miniaturas --------------------------------

// coverCache evita extraer la misma carátula cada vez que se redibuja la
// cuadrícula. La clave es la carpeta; se invalida si cambia el fichero
// origen (ruta + fecha de modificación).
type coverCache struct {
	mu      sync.Mutex
	entries map[string]coverEntry
}

type coverEntry struct {
	source string // ruta del fichero del que salió + mtime
	data   string // data URL
}

var covers = coverCache{entries: make(map[string]coverEntry)}

// AlbumCover devuelve la carátula de un disco como data URL: cover.jpg si
// existe; si no, la imagen incrustada en el primer fichero de audio. Cadena
// vacía si no hay ninguna.
func (a *App) AlbumCover(dir string) (string, error) {
	src, data, err := findCover(a.ctx, dir)
	if err != nil {
		return "", err
	}
	if src == "" {
		return "", nil
	}
	covers.mu.Lock()
	defer covers.mu.Unlock()
	if e, ok := covers.entries[dir]; ok && e.source == src {
		return e.data, nil
	}
	url := "data:" + sniffImageType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
	covers.entries[dir] = coverEntry{source: src, data: url}
	return url, nil
}

// findCover localiza la carátula de la carpeta y devuelve (clave de caché,
// bytes). Solo lee los bytes si hace falta (clave distinta a la cacheada).
func findCover(ctx context.Context, dir string) (string, []byte, error) {
	if p := filepath.Join(dir, "cover.jpg"); fileExists(p) {
		info, _ := os.Stat(p)
		key := p + "|" + info.ModTime().String()
		if cached(dir, key) {
			return key, nil, nil
		}
		data, err := os.ReadFile(p)
		return key, data, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil, nil
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	// El primer fichero que lleve imagen (en carpetas mixtas no todos la
	// tienen). Como mucho miramos 8 para no lanzar ffmpeg veinte veces.
	for i, name := range names {
		if i >= 8 {
			break
		}
		p := filepath.Join(dir, name)
		info, _ := os.Stat(p)
		key := p + "|" + info.ModTime().String()
		if cached(dir, key) {
			return key, nil, nil
		}
		if data, err := embeddedCover(ctx, p); err == nil {
			return key, data, nil
		}
	}
	return "", nil, nil // sin carátula: no es un error para la ventana
}

func cached(dir, key string) bool {
	covers.mu.Lock()
	defer covers.mu.Unlock()
	e, ok := covers.entries[dir]
	return ok && e.source == key
}

// sniffImageType mira los primeros bytes: JPEG empieza por FF D8, PNG por
// 89 50 4E 47.
func sniffImageType(b []byte) string {
	switch {
	case len(b) >= 4 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	default:
		return "image/jpeg"
	}
}

// commonArtist devuelve el artista del disco según las etiquetas: el
// album_artist si lo hay; si no, el artista de las pistas cuando es el mismo
// en (casi) todas. En recopilaciones con muchos artistas devuelve "" para no
// contaminar la búsqueda.
func commonArtist(files []localTrack) string {
	counts := map[string]int{}
	for _, f := range files {
		if f.AlbumArtist != "" {
			return f.AlbumArtist
		}
		if f.Artist != "" {
			counts[f.Artist]++
		}
	}
	if len(counts) >= 3 {
		return ""
	}
	best, n := "", 0
	for a, c := range counts {
		if c > n {
			best, n = a, c
		}
	}
	return best
}

// ---- tarjeta de la cuadrícula ---------------------------------------------

// albumCard es lo que la cuadrícula pide en segundo plano por cada disco:
// carátula y la deducción artista/álbum (nombre de carpeta cotejado con las
// etiquetas), para no enseñar "1998 - Under the..." cuando se sabe que es
// "Frost — Under the Hungarian Blackmoon".
type albumCard struct {
	Cover  string `json:"cover"`
	Artist string `json:"artist"` // artista y álbum tal como se muestran (etiquetas + nombre de la carpeta)
	Album  string `json:"album"`
	// Lo que dicen solo las etiquetas, para proponer el nombre de la carpeta.
	TagArtist string `json:"tagArtist"`
	TagAlbum  string `json:"tagAlbum"`
	Date      string `json:"date"`
	Media     string `json:"media"` // etiqueta media (formato de origen) de las pistas, si la llevan
	MBID      string `json:"mbid"`  // MusicBrainz Album Artist Id de las pistas, si lo llevan
}

type cardCache struct {
	mu      sync.Mutex
	entries map[string]cardEntry
}

type cardEntry struct {
	modTime string // fecha de modificación de la carpeta: si cambia, se recalcula
	card    albumCard
}

var cards = cardCache{entries: make(map[string]cardEntry)}

// AlbumCard devuelve carátula y nombres deducidos de un disco. Solo lee las
// etiquetas de los tres primeros ficheros: basta para deducir y es rápido.
func (a *App) AlbumCard(dir string) (albumCard, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return albumCard{}, err
	}
	mod := info.ModTime().String()
	if card, ok := cachedCard(dir); ok { // memoria o disco (player.go)
		return card, nil
	}

	card := albumCard{}
	// La carátula va como miniatura (200 px) y se guarda en disco: con más
	// de mil discos, la imagen entera en base64 por tarjeta no cabe.
	var thumb string
	if _, data, err := coverBytes(a.ctx, dir); err == nil && len(data) > 0 {
		thumb, card.Cover = thumbnail(a.ctx, dir, data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return card, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	var files []localTrack
	for i, name := range names {
		if i >= 3 {
			break
		}
		if t, err := probe(a.ctx, filepath.Join(dir, name)); err == nil {
			files = append(files, t)
			if card.Media == "" {
				card.Media = t.Media
			}
			if card.MBID == "" {
				card.MBID = t.ArtistMBID
			}
		}
	}
	card.Artist, card.Album = guessNames(filepath.Base(dir), files)
	// Lo que dicen las etiquetas a secas, sin mezclarlo con el nombre de la
	// carpeta: es lo que manda al proponer cómo debería llamarse.
	card.TagArtist = commonArtist(files)
	card.TagAlbum = commonTag(files, func(f localTrack) string { return f.Album })
	card.Date = commonTag(files, func(f localTrack) string { return f.Date })

	rememberCard(dir, mod, card, thumb)
	return card, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// coverBytes lee la carátula de la carpeta (cover.jpg o la incrustada en el
// primer fichero que la lleve) sin pasar por ninguna caché: (clave, bytes).
// Bytes vacíos si no hay.
func coverBytes(ctx context.Context, dir string) (string, []byte, error) {
	if p := filepath.Join(dir, "cover.jpg"); fileExists(p) {
		data, err := os.ReadFile(p)
		return p, data, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	for i, name := range names {
		if i >= 8 {
			break
		}
		p := filepath.Join(dir, name)
		if data, err := embeddedCover(ctx, p); err == nil {
			return p, data, nil
		}
	}
	return "", nil, nil
}
