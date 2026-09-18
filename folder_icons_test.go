//go:build integration

package main

// Iconos de carpeta de verdad en una carpeta temporal (usa ffmpeg de bin/):
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestFolderIcons

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFolderIcons(t *testing.T) {
	app := testApp(t)
	app.settings.FolderIcons = true
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "Oliphant - Songs from the Crusades (1996)")
	os.MkdirAll(dir, 0o755)

	// Una carátula y una pista con etiqueta media=Cassette.
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	f, _ := os.Create(filepath.Join(dir, "cover.jpg"))
	jpeg.Encode(f, img, nil)
	f.Close()
	gen := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "2",
		"-metadata", "title=Chevalier mult estes guariz", "-metadata", "artist=Oliphant", "-metadata", "media=Cassette",
		"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Chevalier.mp3"))
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generando audio: %v %s", err, out)
	}
	// Un desktop.ini ajeno que hay que conservar.
	os.WriteFile(filepath.Join(dir, "desktop.ini"), []byte("[ViewState]\r\nMode=\r\nFolderType=Music\r\n"), 0o644)

	// Automático: la etiqueta media manda -> cassette.
	info, err := app.FolderIcon(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != kindCassette || info.Source != "etiquetas" || info.Exists || !strings.HasPrefix(info.Preview, "data:image/png") {
		t.Fatalf("FolderIcon auto: %+v", info)
	}

	// Crear con el automático.
	info, err = app.SetFolderIcon(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists || info.Kind != kindCassette || info.Fixed {
		t.Fatalf("SetFolderIcon auto: %+v", info)
	}
	ini := readIni(dir)
	if ini.auto != kindCassette || ini.kind != "" || len(ini.rest) != 3 || ini.rest[0] != "[ViewState]" {
		t.Fatalf("desktop.ini: %+v", ini)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "desktop.ini"))
	if raw[0] != 0xFF || raw[1] != 0xFE {
		t.Error("desktop.ini debe ir en UTF-16 con BOM")
	}
	ico, err := os.ReadFile(filepath.Join(dir, iconFileName))
	if err != nil || len(ico) < 10000 || ico[2] != 1 {
		t.Fatalf("folder.ico: %v (%d bytes)", err, len(ico))
	}
	t.Logf("desktop.ini:\n%s", strings.ReplaceAll(string(mustUTF16(t, raw)), "\r", ""))

	// Fijar a vinilo: manda sobre la etiqueta.
	if info, err = app.SetFolderIcon(dir, kindVinyl); err != nil || info.Kind != kindVinyl || !info.Fixed {
		t.Fatalf("fijar vinilo: %v %+v", err, info)
	}
	if k, src, _ := app.resolveKind(dir, "CD"); k != kindVinyl || src != "fijado" {
		t.Errorf("fijado debe mandar sobre la edición: %s/%s", k, src)
	}
	// Volver a automático: lo deja de fijar y vuelve a la etiqueta.
	if info, err = app.SetFolderIcon(dir, ""); err != nil || info.Kind != kindCassette || info.Fixed {
		t.Fatalf("volver a auto: %v %+v", err, info)
	}
	// Sobrescribir un fichero oculto no debe fallar (segunda pasada ya hecha).
	app.autoIcon(dir, "12\" Vinyl")
	if k, src, _ := app.resolveKind(dir, ""); k != kindVinyl || src != "edición" {
		t.Errorf("tras autoIcon con edición en vinilo: %s/%s", k, src)
	}

	// Quitar: se van los nuestros y queda lo ajeno.
	if err := app.RemoveFolderIcon(dir); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(dir, iconFileName)) || hasFolderIcon(dir) {
		t.Error("el icono debería haberse ido")
	}
	if ini = readIni(dir); len(ini.rest) != 3 {
		t.Errorf("se perdió lo ajeno del desktop.ini: %+v", ini)
	}
	// Y una carpeta que se queda solo con nuestros restos se borra entera.
	os.Remove(filepath.Join(dir, "01 - Chevalier.mp3"))
	os.Remove(filepath.Join(dir, "desktop.ini"))
	app.SetFolderIcon(dir, kindCD)
	removeIfEmptyAlbum(dir)
	if fileExists(dir) {
		t.Error("removeIfEmptyAlbum debe borrar carpeta con solo carátula e icono")
	}
}

func mustUTF16(t *testing.T, raw []byte) []byte {
	out, err := utf16.NewDecoder().Bytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFolderIconKeep deja un icono real en la carpeta ICONS_KEEP para
// comprobarlo a mano en el Explorador (no se ejecuta sin la variable).
func TestFolderIconKeep(t *testing.T) {
	root := os.Getenv("ICONS_KEEP")
	if root == "" {
		t.Skip("define ICONS_KEEP")
	}
	app := testApp(t)
	for _, kind := range []string{kindCD, kindVinyl, kindCassette, kindDigital} {
		dir := filepath.Join(root, "Prueba "+kindLabel[kind])
		os.MkdirAll(dir, 0o755)
		img := image.NewRGBA(image.Rect(0, 0, 200, 200))
		for y := 0; y < 200; y++ {
			for x := 0; x < 200; x++ {
				img.Set(x, y, color.RGBA{uint8(255 - x), uint8(y), uint8(x/2 + y/2), 255})
			}
		}
		f, _ := os.Create(filepath.Join(dir, "cover.jpg"))
		jpeg.Encode(f, img, nil)
		f.Close()
		gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
			"-metadata", "title=Prueba", "-metadata", "artist=Prueba", "-metadata", "album="+kindLabel[kind],
			"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Prueba.mp3"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando audio: %v %s", err, out)
		}
		if _, err := app.SetFolderIcon(dir, kind); err != nil {
			t.Fatal(err)
		}
	}
}

// El lote recorre la carpeta entera (discos a cualquier profundidad) y la
// segunda pasada no rehace nada.
func TestMakeFolderIcons(t *testing.T) {
	app := testApp(t)
	root := t.TempDir()
	dirs := []string{
		filepath.Join(root, "Frost - Under the Hungarian Blackmoon (1998)"),
		filepath.Join(root, "Medieval", "Oliphant", "Songs from the Crusades"),
		filepath.Join(root, "_original", "no cuenta"),
	}
	for _, dir := range dirs {
		os.MkdirAll(dir, 0o755)
		gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
			"-metadata", "media=Vinyl", "-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Pista.mp3"))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando audio: %v %s", err, out)
		}
	}
	os.MkdirAll(filepath.Join(root, "Medieval", "vacía"), 0o755)

	var events []iconProgress
	app.emit = func(event string, data ...any) {
		if event == "icons" {
			events = append(events, data[0].(iconProgress))
		}
	}
	p, err := app.MakeFolderIcons(root)
	if err != nil {
		t.Fatal(err)
	}
	// 2 discos + la carpeta de artista "Oliphant" (Medieval no: es solo un contenedor de artistas... y de "vacía").
	if p.Total != 3 || p.Done != 3 || p.Skipped != 0 || len(events) < 2 || events[len(events)-1].Running {
		t.Fatalf("primera pasada: %+v (eventos %d)", p, len(events))
	}
	for _, dir := range dirs[:2] {
		if !hasFolderIcon(dir) || readIni(dir).auto != kindVinyl {
			t.Errorf("%s: sin icono de vinilo (%+v)", filepath.Base(dir), readIni(dir))
		}
	}
	if hasFolderIcon(dirs[2]) {
		t.Error("_original no debe tocarse")
	}
	if p, _ = app.MakeFolderIcons(root); p.Done != 0 || p.Skipped != 3 {
		t.Errorf("segunda pasada debía saltarlos todos: %+v", p)
	}
	// Cambia la carátula: ese disco se rehace.
	f, _ := os.Create(filepath.Join(dirs[0], "cover.jpg"))
	jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 50, 50)), nil)
	f.Close()
	if p, _ = app.MakeFolderIcons(root); p.Done != 1 || p.Skipped != 2 {
		t.Errorf("tras cambiar la carátula: %+v", p)
	}
}
