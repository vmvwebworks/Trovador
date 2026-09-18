//go:build integration

package main

// Llevarse discos al coche sobre carpetas temporales (usa ffmpeg de bin/):
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestCarry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFatName(t *testing.T) {
	cases := map[string]string{
		`AC/DC - Back: In Black?`: "AC_DC - Back_ In Black_",
		"Disco.":                  "Disco",
		"  Dos   espacios ":       "Dos espacios",
		"":                        "_",
	}
	for in, want := range cases {
		if got := fatName(in); got != want {
			t.Errorf("fatName(%q) = %q, esperaba %q", in, got, want)
		}
	}
}

func TestCarry(t *testing.T) {
	app := testApp(t)
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	ctx := context.Background()
	lib := t.TempDir()
	// Un disco en FLAC con carátula y basura de Windows, otro en MP3.
	flacDir := filepath.Join(lib, "Grupo - Disco Uno") // en las etiquetas, «Disco: Uno»: el destino sale sin los dos puntos
	mp3Dir := filepath.Join(lib, "Otro - Dos")
	os.MkdirAll(filepath.Join(flacDir, "_original"), 0o755)
	os.MkdirAll(mp3Dir, 0o755)
	gen := func(path, codec, artist, album string) {
		cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
			"-metadata", "artist="+artist, "-metadata", "album="+album, "-c:a", codec, path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generando %s: %v %s", path, err, out)
		}
	}
	gen(filepath.Join(flacDir, "01 - Una.flac"), "flac", "Grupo", "Disco: Uno")
	gen(filepath.Join(flacDir, "02 - Dos.flac"), "flac", "Grupo", "Disco: Uno")
	gen(filepath.Join(flacDir, "_original", "01 - Una.wav"), "pcm_s16le", "Grupo", "Disco: Uno")
	gen(filepath.Join(mp3Dir, "01 - Tres.mp3"), "libmp3lame", "Otro", "Dos")
	os.WriteFile(filepath.Join(flacDir, "cover.jpg"), []byte("jpg"), 0o644)
	os.WriteFile(filepath.Join(flacDir, "desktop.ini"), []byte("[.ShellClassInfo]"), 0o644)
	os.WriteFile(filepath.Join(flacDir, "folder.ico"), []byte("ico"), 0o644)

	dest := t.TempDir()
	o := carryOptions{Dirs: []string{flacDir, mp3Dir}, Dest: dest, Layout: "flat", Convert: true, Format: "mp3-320"}
	plan, err := app.CarryPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || plan.Files != 3 || !plan.Fits {
		t.Fatalf("plan: %+v", plan)
	}
	if it := plan.Items[0]; it.Convert != 2 || it.Target != filepath.Join(dest, "Grupo - Disco_ Uno") || it.Skip != "" {
		t.Fatalf("plan del flac: %+v", it)
	}
	if it := plan.Items[1]; it.Convert != 0 || it.Files != 1 {
		t.Fatalf("plan del mp3: %+v", it)
	}

	p, err := app.CarryApply(o)
	if err != nil || p.Done != 3 || p.Errors != 0 {
		t.Fatalf("copia: %+v %v", p, err)
	}
	// El FLAC llega como MP3, con la carátula; sin desktop.ini, folder.ico ni _original.
	got := filepath.Join(dest, "Grupo - Disco_ Uno")
	for _, f := range []string{"01 - Una.mp3", "02 - Dos.mp3", "cover.jpg"} {
		if !fileExists(filepath.Join(got, f)) {
			t.Errorf("falta %s", f)
		}
	}
	for _, f := range []string{"01 - Una.flac", "desktop.ini", "folder.ico", "_original"} {
		if _, err := os.Stat(filepath.Join(got, f)); err == nil {
			t.Errorf("sobra %s", f)
		}
	}
	if !fileExists(filepath.Join(dest, "Otro - Dos", "01 - Tres.mp3")) {
		t.Error("falta el mp3 copiado tal cual")
	}
	// Segunda vez: ya están, y se saltan (salvo que se pida repetir).
	plan, _ = app.CarryPlan(o)
	if plan.Files != 0 || !plan.Items[0].Exists || plan.Items[0].Skip == "" {
		t.Fatalf("segundo plan: %+v", plan)
	}
	o.Replace = true
	if plan, _ = app.CarryPlan(o); plan.Files != 3 {
		t.Fatalf("plan repitiendo: %+v", plan)
	}
	// Destino dentro del propio disco: se salta.
	if plan, _ = app.CarryPlan(carryOptions{Dirs: []string{mp3Dir}, Dest: lib, Layout: "flat"}); plan.Items[0].Skip == "" {
		t.Fatalf("no detecta el destino sobre sí mismo: %+v", plan.Items[0])
	}
}
