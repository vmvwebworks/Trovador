package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	xdraw "golang.org/x/image/draw"
)

// Reproductor. El audio lo reproduce la propia ventana (<audio> de
// WebView2); lo que hace falta de Go es servirle los ficheros por HTTP,
// porque el WebView no puede abrir rutas file:// . El servidor de recursos
// de Wails deja pasar a un handler nuestro lo que no es parte de la
// interfaz: /media?p=<ruta> devuelve el fichero con soporte de rangos, que
// es lo que permite saltar dentro de la pista.

var audioMIME = map[string]string{
	".mp3": "audio/mpeg", ".flac": "audio/flac", ".m4a": "audio/mp4", ".opus": "audio/ogg",
	".ogg": "audio/ogg", ".wav": "audio/wav",
	".wma": "audio/x-ms-wma", ".ape": "audio/ape", ".wv": "audio/wavpack", ".aiff": "audio/aiff", ".aif": "audio/aiff", ".mpc": "audio/musepack",
}

// mediaHandler sirve /media?p=<ruta absoluta de un fichero de audio>.
func (a *App) mediaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/media" {
			http.NotFound(w, r)
			return
		}
		p := filepath.Clean(r.URL.Query().Get("p"))
		mime := audioMIME[strings.ToLower(filepath.Ext(p))]
		info, err := os.Stat(p)
		if mime == "" || err != nil || info.IsDir() || !filepath.IsAbs(p) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, p) // gestiona Range, HEAD y Last-Modified
	})
}

// ---- la colección para el reproductor ---------------------------------------

// collectionRoot: la carpeta de la colección (ajuste), o la de descargas.
func (a *App) collectionRoot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.settings.Collection) != "" {
		return a.settings.Collection
	}
	return a.settings.OutDir
}

// ChooseCollectionFolder elige la carpeta de la colección y la guarda.
func (a *App) ChooseCollectionFolder() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Carpeta de la colección", DefaultDirectory: existingDir(a.collectionRoot()),
	})
	if err != nil || dir == "" {
		return "", err
	}
	a.mu.Lock()
	a.settings.Collection = dir
	s := a.settings
	a.mu.Unlock()
	return dir, s.save()
}

// playerAlbum es lo que la cuadrícula del reproductor necesita de un disco.
type playerAlbum struct {
	Dir    string        `json:"dir"`
	Name   string        `json:"name"`   // ruta relativa a la colección
	Artist string        `json:"artist"` // de la caché de tarjetas si ya se conoce; si no, deducido del nombre
	Album  string        `json:"album"`
	Files  int           `json:"files"`
	Cover  string        `json:"cover"`  // miniatura (data URL) si ya está en caché; "" = pedirla con AlbumCard
	Known  bool          `json:"known"`  // la tarjeta viene de caché (no hay que pedirla)
	Tracks []playerTrack `json:"tracks"` // las pistas, por nombre de fichero (sin lanzar ffprobe)
	Date   string        `json:"date"`   // año, si la tarjeta lo sabe
	Kind   string        `json:"kind"`   // cd | vinyl | cassette | digital | "" (lo que ya se sabe sin buscar nada)
}

// playerTrack: una pista tal como se ve sin leer sus etiquetas: el nombre
// del fichero y un título limpio (sin numeración ni "Artista - ").
type playerTrack struct {
	Name  string `json:"name"`  // fichero (con extensión)
	Title string `json:"title"` // para enseñar y filtrar
}

// audioTracks lista los ficheros de audio de la carpeta en orden natural.
func audioTracks(dir string) []playerTrack {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			names = append(names, e.Name())
		}
	}
	sortNatural(names)
	out := make([]playerTrack, 0, len(names))
	for _, n := range names {
		out = append(out, playerTrack{Name: n, Title: trackTitleOf(localTrack{Name: n})})
	}
	return out
}

// PlayerAlbums lista los discos de la colección para la cuadrícula. Es
// rápido: no lanza ffmpeg; lo que ya está en la caché de tarjetas (de
// esta sesión o de disco) sale completo, el resto con nombres deducidos de
// la carpeta y sin carátula, y la ventana los va pidiendo.
func (a *App) PlayerAlbums() ([]playerAlbum, error) {
	root := a.collectionRoot()
	dirs, err := albumDirs(a.ctx, root)
	if err != nil {
		return nil, err
	}
	out := make([]playerAlbum, 0, len(dirs))
	for _, dir := range dirs {
		pa := playerAlbum{Dir: dir, Name: relTo(root, dir), Tracks: audioTracks(dir)}
		pa.Files = len(pa.Tracks)
		// El formato, sin lanzar nada: lo fijado o anotado en el desktop.ini del
		// icono, y si no, la etiqueta media de la tarjeta (más abajo).
		if ini := readIni(dir); ini.kind != "" && ini.kind != kindArtist {
			pa.Kind = ini.kind
		} else if k := mediaKind(ini.format); k != "" {
			pa.Kind = k
		} else if ini.auto != "" {
			pa.Kind = ini.auto
		}
		if card, ok := cachedCard(dir); ok {
			pa.Artist, pa.Album, pa.Cover, pa.Date, pa.Known = card.Artist, card.Album, card.Cover, card.Date, true
			if pa.Kind == "" {
				pa.Kind = mediaKind(card.Media)
			}
		} else {
			pa.Artist, pa.Album = guessNames(filepath.Base(dir), nil)
			if pa.Artist == "" {
				pa.Artist = filepath.Base(filepath.Dir(dir)) // la carpeta del artista
			}
		}
		out = append(out, pa)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Artist, out[j].Artist) {
			return naturalLess(out[i].Artist, out[j].Artist)
		}
		return naturalLess(out[i].Album, out[j].Album)
	})
	return out, nil
}

// PlayerTracks: las pistas de un disco en orden, listas para la cola.
func (a *App) PlayerTracks(dir string) ([]localTrack, error) {
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return nil, err
	}
	return scan.Files, nil
}

// ---- caché persistente de tarjetas y miniaturas -----------------------------

// Las tarjetas (artista, álbum, miniatura de la carátula) se guardan en
// %APPDATA%\Trovador\cards.json y las miniaturas (200 px, JPEG) en
// thumbs\. Con más de mil discos, volver a lanzar ffmpeg y ffprobe por cada
// uno en cada arranque no es de recibo; con la caché, la cuadrícula sale al
// instante desde la segunda vez. La clave es la fecha de modificación de la
// carpeta: si cambia algo dentro, se recalcula.

type cardRecord struct {
	ModTime string `json:"modTime"`
	Artist  string `json:"artist"`
	Album   string `json:"album"`
	Media   string `json:"media,omitempty"`
	MBID    string `json:"mbid,omitempty"`
	Thumb   string `json:"thumb,omitempty"` // fichero en thumbs\ (vacío: sin carátula)
	// Lo que dicen solo las etiquetas (ver albumCard).
	TagArtist string `json:"tagArtist,omitempty"`
	TagAlbum  string `json:"tagAlbum,omitempty"`
	Date      string `json:"date,omitempty"`
}

type cardStore struct {
	mu      sync.Mutex
	loaded  bool
	dirty   bool
	records map[string]cardRecord
	saving  *time.Timer
}

var cardDisk = cardStore{records: map[string]cardRecord{}}

func cacheDir() string {
	if d := os.Getenv("MUSIC_PICKER_CACHE"); d != "" { // los tests no tocan la caché real
		return d
	}
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return dir
}

func (s *cardStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true
	if dir := cacheDir(); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "cards.json")); err == nil {
			_ = json.Unmarshal(data, &s.records)
		}
	}
	if s.records == nil {
		s.records = map[string]cardRecord{}
	}
}

// put guarda el registro y programa el volcado a disco (con un respiro:
// en un lote llegan cientos seguidos).
func (s *cardStore) put(dir string, rec cardRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[dir] = rec
	s.dirty = true
	if s.saving == nil {
		s.saving = time.AfterFunc(2*time.Second, s.flush)
	} else {
		s.saving.Reset(2 * time.Second)
	}
}

func (s *cardStore) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	data, err := json.Marshal(s.records)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return
	}
	if dir := cacheDir(); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "cards.json"), data, 0o644)
	}
}

// cachedCard devuelve la tarjeta si está en memoria o en disco y la carpeta
// no ha cambiado desde entonces.
func cachedCard(dir string) (albumCard, bool) {
	info, err := os.Stat(dir)
	if err != nil {
		return albumCard{}, false
	}
	mod := info.ModTime().String()
	cards.mu.Lock()
	if e, ok := cards.entries[dir]; ok && e.modTime == mod {
		cards.mu.Unlock()
		return e.card, true
	}
	cards.mu.Unlock()

	cardDisk.load()
	cardDisk.mu.Lock()
	rec, ok := cardDisk.records[dir]
	cardDisk.mu.Unlock()
	if !ok || rec.ModTime != mod {
		return albumCard{}, false
	}
	card := albumCard{Artist: rec.Artist, Album: rec.Album, Media: rec.Media, MBID: rec.MBID, TagArtist: rec.TagArtist, TagAlbum: rec.TagAlbum, Date: rec.Date}
	if rec.Thumb != "" {
		data, err := os.ReadFile(filepath.Join(cacheDir(), "thumbs", rec.Thumb))
		if err != nil {
			return albumCard{}, false // miniatura perdida: se recalcula
		}
		card.Cover = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	}
	cards.mu.Lock()
	cards.entries[dir] = cardEntry{modTime: mod, card: card}
	cards.mu.Unlock()
	return card, true
}

// rememberCard guarda la tarjeta en memoria y en disco.
func rememberCard(dir, mod string, card albumCard, thumb string) {
	cards.mu.Lock()
	cards.entries[dir] = cardEntry{modTime: mod, card: card}
	cards.mu.Unlock()
	cardDisk.load()
	cardDisk.put(dir, cardRecord{ModTime: mod, Artist: card.Artist, Album: card.Album, Media: card.Media, MBID: card.MBID, Thumb: thumb,
		TagArtist: card.TagArtist, TagAlbum: card.TagAlbum, Date: card.Date})
}

// thumbnail hace la miniatura (200 px, JPEG) de una carátula, la guarda en
// thumbs\ y devuelve (nombre de fichero, data URL). Si la imagen no se
// puede decodificar, se devuelve la original tal cual sin guardar nada.
func thumbnail(ctx context.Context, dir string, data []byte) (file, dataURL string) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if raw, err2 := ffmpegToPNG(ctx, data); err2 == nil {
			img, _, err = image.Decode(bytes.NewReader(raw))
		}
	}
	if err != nil {
		return "", "data:" + sniffImageType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	const side = 200
	b := img.Bounds()
	w, h := side, side
	if b.Dx() > b.Dy() {
		h = side * b.Dy() / b.Dx()
	} else if b.Dy() > b.Dx() {
		w = side * b.Dx() / b.Dy()
	}
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(small, small.Bounds(), img, b, xdraw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 82}); err != nil {
		return "", ""
	}
	sum := sha1.Sum([]byte(dir))
	file = hex.EncodeToString(sum[:8]) + ".jpg"
	if cache := cacheDir(); cache != "" {
		_ = os.MkdirAll(filepath.Join(cache, "thumbs"), 0o755)
		_ = os.WriteFile(filepath.Join(cache, "thumbs", file), buf.Bytes(), 0o644)
	}
	return file, "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
