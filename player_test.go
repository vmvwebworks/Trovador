//go:build integration

package main

// Reproductor: servir audio con rangos, y la caché de tarjetas en disco.
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run 'TestMedia|TestCardCache'

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeAlbum(t *testing.T, root, artist, album string, n int, withCover bool) string {
	t.Helper()
	dir := filepath.Join(root, artist, album)
	os.MkdirAll(dir, 0o755)
	for i := 1; i <= n; i++ {
		gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "3",
			"-metadata", "artist="+artist, "-metadata", "album="+album, "-metadata", "title=Pista "+string(rune('0'+i)),
			"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "0"+string(rune('0'+i))+" - Pista.mp3"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando audio: %v %s", err, out)
		}
	}
	if withCover {
		img := image.NewRGBA(image.Rect(0, 0, 600, 600))
		for y := 0; y < 600; y++ {
			for x := 0; x < 600; x++ {
				img.Set(x, y, color.RGBA{uint8(x / 3), uint8(y / 3), 90, 255})
			}
		}
		f, _ := os.Create(filepath.Join(dir, "cover.jpg"))
		jpeg.Encode(f, img, nil)
		f.Close()
	}
	return dir
}

func TestMedia(t *testing.T) {
	app := testApp(t)
	root := t.TempDir()
	dir := makeAlbum(t, root, "Prueba", "Disco", 1, false)
	path := filepath.Join(dir, "01 - Pista.mp3")
	srv := httptest.NewServer(app.mediaHandler())
	defer srv.Close()
	get := func(p string, rng string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/media?p="+url.QueryEscape(p), nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get(path, "")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("entero: %d %s ranges=%q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Accept-Ranges"))
	}
	resp.Body.Close()
	if resp = get(path, "bytes=100-199"); resp.StatusCode != 206 || resp.ContentLength != 100 {
		t.Errorf("rango: %d len %d", resp.StatusCode, resp.ContentLength)
	}
	resp.Body.Close()
	// Lo que no es audio, o no existe, no se sirve.
	if resp = get(filepath.Join(dir, "cover.jpg"), ""); resp.StatusCode != 404 {
		t.Errorf("no audio: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if resp = get(filepath.Join(dir, "nada.mp3"), ""); resp.StatusCode != 404 {
		t.Errorf("inexistente: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCardCache(t *testing.T) {
	app := testApp(t)
	cache := t.TempDir()
	t.Setenv("MUSIC_PICKER_CACHE", cache)
	root := t.TempDir()
	app.settings.Collection = root
	a := makeAlbum(t, root, "Oliphant", "Songs from the Crusades", 2, true)
	b := makeAlbum(t, root, "Frost", "Under the Hungarian Blackmoon", 1, false)

	// Primera vez: nada en caché.
	albums, err := app.PlayerAlbums()
	if err != nil || len(albums) != 2 || albums[0].Known || albums[1].Known {
		t.Fatalf("PlayerAlbums: %v %+v", err, albums)
	}
	card, err := app.AlbumCard(a)
	if err != nil || card.Artist != "Oliphant" || !strings.HasPrefix(card.Cover, "data:image/jpeg") || len(card.Cover) > 60000 {
		t.Fatalf("tarjeta: %v artista=%q cover=%d bytes", err, card.Artist, len(card.Cover))
	}
	if _, err := app.AlbumCard(b); err != nil {
		t.Fatal(err)
	}
	cardDisk.flush()
	if !fileExists(filepath.Join(cache, "cards.json")) {
		t.Fatal("cards.json no se ha escrito")
	}
	thumbs, _ := os.ReadDir(filepath.Join(cache, "thumbs"))
	if len(thumbs) != 1 {
		t.Errorf("miniaturas: %d (esperaba 1: el disco sin carátula no tiene)", len(thumbs))
	}

	// "Otro arranque": caché en memoria vacía, todo sale del disco.
	cards.entries = map[string]cardEntry{}
	cardDisk = cardStore{records: map[string]cardRecord{}}
	albums, _ = app.PlayerAlbums()
	for _, al := range albums {
		if !al.Known {
			t.Errorf("%s debería salir de la caché en disco", al.Name)
		}
	}
	if albums[0].Artist != "Frost" || albums[1].Album != "Songs from the Crusades" || albums[1].Cover == "" {
		t.Errorf("orden o datos: %+v", albums)
	}
	// Cambia la carpeta (un fichero más): se recalcula.
	os.WriteFile(filepath.Join(b, "cover.jpg"), []byte("x"), 0o644)
	os.Chtimes(b, timeNow(), timeNow())
	if c, _ := cachedCard(b); c.Artist != "" {
		t.Error("tras cambiar la carpeta la caché no vale")
	}
	tracks, err := app.PlayerTracks(a)
	if err != nil || len(tracks) != 2 || tracks[0].Title != "Pista 1" {
		t.Errorf("pistas: %v %+v", err, tracks)
	}
}

func timeNow() time.Time { return time.Now().Add(time.Second) }

func TestFavorites(t *testing.T) {
	app := testApp(t)
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	favs = favStore{}
	root := t.TempDir()
	a := makeAlbum(t, root, "Oliphant", "Songs from the Crusades", 1, false)
	b := makeAlbum(t, root, "Frost", "Under the Hungarian Blackmoon", 1, false)
	if l, _ := app.Favorites(); len(l) != 0 {
		t.Fatalf("de entrada vacío: %v", l)
	}
	if l, err := app.SetFavorite(a, true); err != nil || len(l) != 1 {
		t.Fatalf("marcar: %v %v", err, l)
	}
	app.SetFavorite(b, true)
	app.SetFavorite(a, true) // repetido: no duplica
	if l, _ := app.Favorites(); len(l) != 2 || l[0] != b || l[1] != a {
		t.Fatalf("dos favoritos ordenados: %v", l)
	}
	// Se mueve la carpeta: el favorito la sigue.
	moved := filepath.Join(root, "Oliphant", "Songs from the Crusades (2000)")
	os.Rename(a, moved)
	favMove(a, moved)
	if l, _ := app.Favorites(); len(l) != 2 || l[1] != moved {
		t.Fatalf("tras mover: %v", l)
	}
	// Otro arranque: se lee del disco.
	favs = favStore{}
	if l, _ := app.Favorites(); len(l) != 2 {
		t.Fatalf("tras recargar: %v", l)
	}
	if l, _ := app.SetFavorite(moved, false); len(l) != 1 || l[0] != b {
		t.Fatalf("desmarcar: %v", l)
	}
}

func TestCoverCrops(t *testing.T) {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	crops = cropStore{}
	app := &App{}
	a := filepath.Join(t.TempDir(), "Artista", "Disco")
	if m, _ := app.CoverCrops(); len(m) != 0 {
		t.Fatalf("de entrada, sin encuadres: %v", m)
	}
	c := coverCrop{X: 0.2, Y: 0, W: 0.8, H: 1}
	if err := app.SetCoverCrop(a, true, c); err != nil {
		t.Fatal(err)
	}
	if m, _ := app.CoverCrops(); m[a] != c {
		t.Fatalf("no se guardó: %v", m)
	}
	// Sigue a la carpeta al moverla (favMove lo arrastra), y se quita.
	moved := filepath.Join(filepath.Dir(a), "Disco (2001)")
	favMove(a, moved)
	if m, _ := app.CoverCrops(); m[moved] != c || len(m) != 1 {
		t.Fatalf("tras mover: %v", m)
	}
	crops = cropStore{} // se relee del disco
	if m, _ := app.CoverCrops(); m[moved] != c {
		t.Fatalf("no persiste: %v", m)
	}
	if err := app.SetCoverCrop(moved, false, coverCrop{}); err != nil {
		t.Fatal(err)
	}
	if m, _ := app.CoverCrops(); len(m) != 0 {
		t.Fatalf("no se quitó: %v", m)
	}
}
