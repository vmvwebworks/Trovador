//go:build integration

package main

// Test de integración: descarga de verdad, así que necesita red y las
// herramientas en bin/. Por eso lleva la etiqueta `integration` y no se
// ejecuta con un `go test` normal. Para lanzarlo:
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestDownloadReal
//
// Los tests en Go son funciones TestXxx(t *testing.T) en ficheros _test.go
// del mismo paquete: ven todo lo no exportado sin ceremonias.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadReal(t *testing.T) {
	if _, err := os.Stat(toolPath("yt-dlp.exe")); err != nil {
		t.Skip("no hay yt-dlp en", binDir())
	}

	// t.TempDir() se borra solo al terminar el test.
	out := t.TempDir()
	cfg := config{outDir: out, workers: 2, format: "mp3", quality: "0"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	urls := []string{
		"https://www.youtube.com/watch?v=aCRViLMtQOA", // disco con capítulos (18 min)
		"https://www.youtube.com/watch?v=1snViidbzBI", // vídeo suelto corto
	}
	results := downloadAll(ctx, urls, cfg, func(line string) { t.Log(line) })

	for _, r := range results {
		if r.err != nil {
			t.Errorf("%s: %v", r.url, r.err)
		}
	}

	var files []string
	filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".mp3" {
			rel, _ := filepath.Rel(out, p)
			files = append(files, rel)
		}
		return nil
	})
	for _, f := range files {
		t.Log("fichero:", f)
	}
	// 4 pistas del disco troceado + 1 vídeo suelto; el fichero largo no debe estar.
	if len(files) != 5 {
		t.Errorf("esperaba 5 mp3, hay %d", len(files))
	}
}
