//go:build integration

package main

// Ordenar por artista sobre carpetas temporales (usa ffmpeg de bin/):
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestOrganize

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFirstArtist(t *testing.T) {
	cases := map[string]string{
		"Oliphant":                    "Oliphant",
		"Oliphant / Sequentia":        "Oliphant",
		"Simon & Garfunkel":           "Simon & Garfunkel",
		"Earth, Wind & Fire":          "Earth, Wind & Fire",
		"Nas feat. Lauryn Hill":       "Nas",
		"Artist ft Other":             "Artist",
		"Metallica vs. Megadeth":      "Metallica",
		"Ensemble Gilles Binchois; X": "Ensemble Gilles Binchois",
	}
	for in, want := range cases {
		if got := firstArtist(in); got != want {
			t.Errorf("firstArtist(%q) = %q, esperaba %q", in, got, want)
		}
	}
	if artistKey("The Cure") != artistKey("Cure, The") || artistKey("Oliphant") == artistKey("Sequentia") {
		t.Error("artistKey")
	}
}

func TestOrganize(t *testing.T) {
	app := testApp(t)
	root := t.TempDir()
	mk := func(rel, artist, album string) string {
		dir := filepath.Join(root, rel)
		os.MkdirAll(dir, 0o755)
		gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
			"-metadata", "artist="+artist, "-metadata", "album_artist="+artist, "-metadata", "album="+album,
			"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Pista.mp3"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando audio: %v %s", err, out)
		}
		return dir
	}
	a := mk("Oliphant - Songs from the Crusades (1996)", "Oliphant", "Songs from the Crusades")
	b := mk("Split (2001)", "Oliphant / Sequentia", "Split")
	c := mk("the cure/Disintegration (1989)", "The Cure", "Disintegration") // ya está en su carpeta (grafía distinta)
	d := mk("Wish (1992)", "Cure, The", "Wish")                             // carpeta existente "the cure" manda
	e := mk("Colisión (2000)", "Oliphant", "Colisión")
	f := mk("Sequentia - Vox Iberica (1992)", "Sequentia", "Vox Iberica")  // artista sin carpeta: se crea
	g := mk("Lost Castles", "Lost Castles", "Lost Castles")                // disco homónimo: jamás dentro de sí mismo
	os.MkdirAll(filepath.Join(root, "Oliphant", "Colisión (2000)"), 0o755) // ya hay una con ese nombre
	if _, err := app.SetFolderIcon(a, kindCD); err != nil {                // carpeta de solo lectura (icono)
		t.Fatal(err)
	}

	plan, err := app.OrganizePlan([]string{a, b, c, d, e, f, g}, root)
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string]organizeItem{}
	for _, it := range plan {
		byDir[it.Dir] = it
		t.Logf("%s -> artista %q (%s) destino %q omitido %q", it.Name, it.Artist, it.Source, it.Target, it.Skip)
	}
	if it := byDir[a]; it.Skip != "" || it.Artist != "Oliphant" || it.NewArtist || it.Target != filepath.Join(root, "Oliphant", filepath.Base(a)) {
		t.Errorf("a: %+v", it)
	}
	if it := byDir[b]; it.Skip != "" || it.Artist != "Oliphant" || it.NewArtist {
		t.Errorf("b (varios artistas): %+v", it) // Oliphant ya está planificada por a
	}
	if it := byDir[c]; it.Skip == "" {
		t.Errorf("c debía quedarse: %+v", it)
	}
	if it := byDir[d]; it.Skip != "" || it.Artist != "the cure" || it.NewArtist {
		t.Errorf("d debía ir a la carpeta existente 'the cure': %+v", it)
	}
	if it := byDir[e]; it.Skip == "" {
		t.Errorf("e debía omitirse por colisión: %+v", it)
	}
	if it := byDir[f]; it.Skip != "" || !it.NewArtist || it.ArtistDir != filepath.Join(root, "Sequentia") {
		t.Errorf("f debía crear la carpeta Sequentia: %+v", it)
	}
	if it := byDir[g]; it.Skip == "" {
		t.Errorf("g (homónimo) debía omitirse: %+v", it)
	}

	n, err := app.OrganizeApply(plan)
	if err != nil || n != 4 {
		t.Fatalf("movidos %d, err %v", n, err)
	}
	for _, want := range []string{
		filepath.Join(root, "Oliphant", "Oliphant - Songs from the Crusades (1996)", "01 - Pista.mp3"),
		filepath.Join(root, "Oliphant", "Split (2001)", "01 - Pista.mp3"),
		filepath.Join(root, "the cure", "Wish (1992)", "01 - Pista.mp3"),
		filepath.Join(root, "the cure", "Disintegration (1989)", "01 - Pista.mp3"),
		filepath.Join(root, "Colisión (2000)", "01 - Pista.mp3"),
		filepath.Join(root, "Sequentia", "Sequentia - Vox Iberica (1992)", "01 - Pista.mp3"),
	} {
		if !fileExists(want) {
			t.Errorf("falta %s", want)
		}
	}
	if fileExists(a) || fileExists(b) || fileExists(d) {
		t.Error("los orígenes deberían haberse ido")
	}
	if !fileExists(filepath.Join(g, "01 - Pista.mp3")) || fileExists(filepath.Join(g, "Lost Castles")) {
		t.Error("el homónimo debe quedarse tal cual, sin copia dentro")
	}
	if err := moveDir(context.Background(), g, filepath.Join(g, "Lost Castles")); err == nil {
		t.Error("moveDir debe negarse a meter una carpeta dentro de sí misma")
	}
	if !hasFolderIcon(filepath.Join(root, "Oliphant", "Oliphant - Songs from the Crusades (1996)")) {
		t.Error("el icono debe viajar con la carpeta")
	}
}
