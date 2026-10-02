//go:build integration

package main

// Iconos de artista contra las fuentes reales (TheAudioDB, Deezer, Bandcamp,
// MusicBrainz) en carpetas temporales:
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestArtistIcons

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArtistIcons(t *testing.T) {
	app := testApp(t)
	root := t.TempDir()
	mk := func(artist, album string) string {
		dir := filepath.Join(root, artist, album)
		os.MkdirAll(dir, 0o755)
		gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
			"-metadata", "artist="+artist, "-metadata", "album_artist="+artist, "-metadata", "album="+album,
			"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Pista.mp3"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando audio: %v %s", err, out)
		}
		return filepath.Dir(dir)
	}
	metallica := mk("Metallica", "Master of Puppets") // conocido: MBID -> TheAudioDB (logo)
	oliphant := mk("Oliphant", "Songs from the Crusades")
	nobody := mk("Zzzq Inexistente", "Qqq Disco Falso") // nadie lo tiene: abanico

	p, err := app.MakeFolderIcons(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 6 || p.Done != 6 {
		t.Errorf("progreso: %+v", p)
	}
	for _, dir := range []string{metallica, oliphant, nobody} {
		ini := readIni(dir)
		if !hasFolderIcon(dir) || ini.kind != kindArtist {
			t.Errorf("%s: sin icono de artista (%+v)", filepath.Base(dir), ini)
		}
		pic, ok := localArtistImage(dir)
		t.Logf("%s: imagen %v (%s, %d bytes)", filepath.Base(dir), ok, pic.source, len(pic.data))
	}
	if pic, ok := localArtistImage(metallica); !ok || !pic.logo {
		t.Errorf("Metallica debería tener logo (TheAudioDB): %v %s", ok, pic.source)
	}
	if _, ok := localArtistImage(oliphant); !ok {
		t.Error("Oliphant debería tener foto (Deezer o Bandcamp)")
	}
	if _, ok := localArtistImage(nobody); ok {
		t.Error("el inexistente no debería tener imagen")
	}
	// Segunda pasada: todo al día, sin red.
	if p, _ = app.MakeFolderIcons(root); p.Skipped != 6 {
		t.Errorf("segunda pasada: %+v", p)
	}
	// Imagen propia: manda y cambia la firma.
	data, _ := os.ReadFile(filepath.Join(oliphant, "folder.jpg"))
	os.WriteFile(filepath.Join(nobody, "artist.jpg"), data, 0o644)
	if p, _ = app.MakeFolderIcons(root); p.Done != 1 || p.Skipped != 5 {
		t.Errorf("con imagen propia nueva: %+v", p)
	}
}

// TestArtistIconKeep deja las carpetas de artista en ARTIST_KEEP para
// mirarlas en el Explorador.
func TestArtistIconKeep(t *testing.T) {
	root := os.Getenv("ARTIST_KEEP")
	if root == "" {
		t.Skip("define ARTIST_KEEP")
	}
	app := testApp(t)
	for _, pair := range [][2]string{{"Metallica", "Master of Puppets"}, {"Oliphant", "Songs from the Crusades"}, {"Frost", "Under the Hungarian Blackmoon"}} {
		dir := filepath.Join(root, pair[0], pair[1])
		os.MkdirAll(dir, 0o755)
		gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
			"-metadata", "artist="+pair[0], "-metadata", "album="+pair[1],
			"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Pista.mp3"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando audio: %v %s", err, out)
		}
	}
	if _, err := app.MakeFolderIcons(root); err != nil {
		t.Fatal(err)
	}
}
