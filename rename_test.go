//go:build integration

package main

// El renombrado en lote con ficheros de verdad (ffmpeg escribe las
// etiquetas, ffprobe las lee). Sin red: la propuesta sale de las etiquetas.
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestRename

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mkAlbum crea una carpeta de disco con una pista etiquetada.
func mkAlbum(t *testing.T, root, folder, artist, album, year string) string {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gen := exec.CommandContext(context.Background(), toolPath("ffmpeg.exe"), "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1",
		"-metadata", "artist="+artist, "-metadata", "album_artist="+artist,
		"-metadata", "album="+album, "-metadata", "date="+year,
		"-c:a", "libmp3lame", "-q:a", "9", filepath.Join(dir, "01 - Pista.mp3"))
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generando audio: %v %s", err, out)
	}
	return dir
}

func TestRenamePlan(t *testing.T) {
	app := testApp(t)
	root := t.TempDir()
	app.libRoot = root // la carpeta que se está mirando en la Biblioteca

	// Los cuatro casos que importan: un nombre de descarga con basura, uno
	// que ya está bien, dos que chocarían entre sí, y uno cuyo nombre nuevo
	// ya existe al lado.
	sucio := mkAlbum(t, root, "[1994] Tears of the Weeping Willow", "Cernunnos Woods", "Tears of the Weeping Willow", "1994")
	bueno := mkAlbum(t, root, "Bathory - Blood Fire Death (1988)", "Bathory", "Blood Fire Death", "1988")
	choque1 := mkAlbum(t, root, "grim - fard 1995", "Grim", "Färd", "1995")
	choque2 := mkAlbum(t, root, "Grim_-_Fard-1995-XYZ", "Grim", "Färd", "1995")
	ocupado := mkAlbum(t, root, "Pest - Desecration RAW", "Pest", "Desecration", "2004")
	if err := os.MkdirAll(filepath.Join(root, "Pest - Desecration (2004)"), 0o755); err != nil {
		t.Fatal(err)
	}

	items, err := app.RenamePlan([]string{sucio, bueno, choque1, choque2, ocupado})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("esperaba 5 propuestas, hay %d", len(items))
	}
	by := map[string]renameItem{}
	for _, it := range items {
		by[it.Name] = it
		t.Logf("%-38s -> %-42s %s %s", it.Name, it.Target, it.Source, it.Skip)
	}

	if it := by["[1994] Tears of the Weeping Willow"]; it.Skip != "" || it.Target != "Cernunnos Woods - Tears of the Weeping Willow (1994)" {
		t.Errorf("nombre sucio: target=%q skip=%q", it.Target, it.Skip)
	} else if it.Source != "etiquetas" {
		t.Errorf("debería salir de las etiquetas, no de %q", it.Source)
	}
	if it := by["Bathory - Blood Fire Death (1988)"]; it.Skip == "" {
		t.Errorf("el que ya se llama bien no debería cambiar, y propone %q", it.Target)
	}
	// De los dos que acabarían igual, uno se renombra y el otro se queda.
	c1, c2 := by["grim - fard 1995"], by["Grim_-_Fard-1995-XYZ"]
	if c1.Target != c2.Target {
		t.Errorf("los dos deberían proponer el mismo nombre: %q y %q", c1.Target, c2.Target)
	}
	if (c1.Skip == "") == (c2.Skip == "") {
		t.Errorf("uno de los dos tiene que quedarse fuera: %q / %q", c1.Skip, c2.Skip)
	}
	if it := by["Pest - Desecration RAW"]; it.Skip == "" {
		t.Error("no debería renombrar encima de una carpeta que ya existe")
	}
	// Quitar "FLAC" o el año repetido no es perder nada; perder una palabra
	// del título sí, y hay que avisar.
	if it := by["[1994] Tears of the Weeping Willow"]; it.Warn != "" {
		t.Errorf("quitar el año de delante no debería avisar: %q", it.Warn)
	}
	// Dentro de la carpeta de su artista, el nombre es el título a secas:
	// repetir el artista y el año no dice nada que no esté ya en la ruta. Y
	// quitarlos no cuenta como pérdida, así que no debe avisar.
	dentro := mkAlbum(t, root, filepath.Join("Bathory", "Bathory - Hammerheart (1990) FLAC"), "Bathory", "Hammerheart", "1990")
	in, err := app.RenamePlan([]string{dentro})
	if err != nil {
		t.Fatal(err)
	}
	if in[0].Target != "Hammerheart" || in[0].Skip != "" {
		t.Errorf("dentro de Bathory\\ debería quedarse en «Hammerheart»: target=%q skip=%q", in[0].Target, in[0].Skip)
	}
	if in[0].Warn != "" {
		t.Errorf("quitar el artista que ya está en la ruta no es perder nada: %q", in[0].Warn)
	}
	// La carpeta del artista puede llamarse cualquier cosa —aquí, como el
	// disco— y las etiquetas decir otra: lo que manda es que cuelgue de la
	// raíz de la Biblioteca, no cómo se llame.
	raro := mkAlbum(t, root, filepath.Join("anthopophobia", "wolfkhan - anthopophobia"), "Wolfkhan", "Anthopophobia", "1994")
	rr, err := app.RenamePlan([]string{raro})
	if err != nil {
		t.Fatal(err)
	}
	if rr[0].Target != "Anthopophobia" {
		t.Errorf("dentro de una carpeta de artista no debe colarse ni el artista ni el año: %q", rr[0].Target)
	}

	// La misma edición en una carpeta suelta (la de descargas) sí lo lleva
	// entero, que allí no hay ruta que lo diga.
	suelto := mkAlbum(t, root, "hammerheart 1990 bathory", "Bathory", "Hammerheart", "1990")
	out, err := app.RenamePlan([]string{suelto})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Target != "Bathory - Hammerheart (1990)" {
		t.Errorf("suelto debería ser «Bathory - Hammerheart (1990)», y es %q", out[0].Target)
	}

	perdida := mkAlbum(t, root, "Wolfkhan - Anthopophobia", "Wolfkhan", "Wolfkhan", "1994")
	one, err := app.RenamePlan([]string{perdida})
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Warn == "" || !strings.Contains(one[0].Warn, "anthopophobia") {
		t.Errorf("debería avisar de que se pierde «anthopophobia»: warn=%q target=%q", one[0].Warn, one[0].Target)
	}

	// Aplicar solo lo que no está omitido, y comprobarlo en el disco.
	var picks []renameItem
	for _, it := range items {
		if it.Skip == "" {
			picks = append(picks, it)
		}
	}
	n, err := app.RenameApply(picks)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(picks) {
		t.Errorf("renombradas %d de %d", n, len(picks))
	}
	for _, it := range picks {
		if !fileExists(filepath.Join(root, it.Target)) {
			t.Errorf("no está la carpeta nueva %q", it.Target)
		}
		if fileExists(it.Dir) {
			t.Errorf("sigue estando la vieja %q", it.Name)
		}
	}
	// Lo omitido se queda tal cual.
	if !fileExists(ocupado) {
		t.Error("una carpeta omitida no se debe tocar")
	}

	// Segunda pasada: ya no hay nada que cambiar salvo lo que se omitió.
	again, err := app.RenamePlan([]string{filepath.Join(root, by["[1994] Tears of the Weeping Willow"].Target), bueno})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range again {
		if it.Skip == "" {
			t.Errorf("%s: no debería volver a proponer cambio (%s)", it.Name, it.Target)
		}
	}
}

// TestRenamePlanReal enseña qué propondría el renombrado sobre una
// biblioteca de verdad, sin tocar nada. Es el ensayo antes de pulsar el
// botón:
//
//	RENAME_ROOT=D:\Music go test -tags integration -v -run TestRenamePlanReal
func TestRenamePlanReal(t *testing.T) {
	root := os.Getenv("RENAME_ROOT")
	if root == "" {
		t.Skip("define RENAME_ROOT con la carpeta de la biblioteca")
	}
	app := testApp(t)
	app.libRoot = root // como si la Biblioteca estuviera mirando esa carpeta
	dirs, err := albumDirs(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := app.RenamePlan(dirs)
	if err != nil {
		t.Fatal(err)
	}
	change, warn, skip := 0, 0, map[string]int{}
	for _, it := range items {
		if it.Skip == "" {
			change++
			mark := "     "
			if it.Warn != "" {
				warn++
				mark = "AVISO"
			}
			t.Logf("%s %-46.46s -> %-46.46s (%s) %s", mark, it.Name, it.Target, it.Source, it.Warn)
		} else {
			skip[it.Skip]++
		}
	}
	t.Logf("TOTAL: %d de %d carpetas cambiarían de nombre, %d con aviso", change, len(items), warn)
	for reason, n := range skip {
		t.Logf("  se quedan %3d: %s", n, reason)
	}
}
