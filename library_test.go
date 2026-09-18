//go:build integration

package main

// Test de integración de la Biblioteca: habla con MusicBrainz y Cover Art
// Archive de verdad, y usa ffmpeg/ffprobe de bin/. Lanzar con:
//
//	MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestLibrary

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testApp construye un App sin ventana: los eventos van al log del test.
func testApp(t *testing.T) *App {
	t.Helper()
	if _, err := os.Stat(toolPath("ffprobe.exe")); err != nil {
		t.Skip("no hay ffprobe en", binDir())
	}
	return &App{
		ctx:  context.Background(),
		emit: func(event string, data ...any) { t.Log(event, data) },
	}
}

func TestLibrary(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()

	// 1. Buscar una edición conocida.
	res, err := app.SearchReleases("Cloudkicker", "Beacons")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		t.Log("aviso:", w)
	}
	// Nos quedamos con la primera de MusicBrainz para el resto del test.
	var rels []mbRelease
	for _, r := range res.Results {
		t.Logf("candidata [%s] %s - %s (%s, %d pistas)", r.Source, r.Artist, r.Title, r.Date, r.TrackCount)
		if r.Source == srcMusicBrainz {
			rels = append(rels, r)
		}
	}
	if len(rels) == 0 {
		t.Fatal("MusicBrainz no devuelve ediciones de Cloudkicker - Beacons")
	}
	pick := rels[0]
	n := pick.TrackCount
	t.Logf("edición: %s (%s, %s, %s)", pick.Title, pick.Date, pick.Country, pick.Format)

	// 2. Tracklist con duraciones.
	rel, err := app.GetRelease(pick.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Tracks) != n || rel.TotalLength < 60 {
		t.Fatalf("tracklist rara: %d pistas, %.0f s", len(rel.Tracks), rel.TotalLength)
	}
	for _, tr := range rel.Tracks {
		if tr.Length == 0 {
			t.Fatalf("pista %d sin duración", tr.Number)
		}
	}

	// 3. Fabricar un "disco entero" sintético con la duración exacta de la
	//    edición (un tono, sin bajar nada) y comprobar el corte.
	dir := t.TempDir()
	long := filepath.Join(dir, "disco entero.mp3")
	gen := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-t", ffTime(rel.TotalLength), "-c:a", "libmp3lame", "-q:a", "9", long)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generando audio: %v %s", err, out)
	}

	// 4. Carátula (antes de cortar, para que las pistas la lleven).
	if _, err := app.FetchCover(dir, rel.Release.ID); err != nil {
		t.Fatal("carátula:", err)
	}
	if st, err := os.Stat(filepath.Join(dir, "cover.jpg")); err != nil || st.Size() < 1000 {
		t.Fatal("cover.jpg no parece una imagen")
	}

	// 5. Separar.
	outs, err := app.SplitFile(long, rel.Release.ID)
	if err != nil {
		t.Fatal("separando:", err)
	}
	if len(outs) != n {
		t.Fatalf("esperaba %d pistas, hay %d", n, len(outs))
	}
	if _, err := os.Stat(filepath.Join(dir, "_original", "disco entero.mp3")); err != nil {
		t.Error("el original no se movió a _original/")
	}

	// 6. Verificar: cada pista dura lo que dice MusicBrainz (±1 s) y lleva
	//    título y carátula.
	scan, err := app.ScanFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Files) != n {
		t.Fatalf("la carpeta tiene %d ficheros", len(scan.Files))
	}
	for i, f := range scan.Files {
		want := rel.Tracks[i]
		if d := math.Abs(f.Duration - want.Length); d > 1 {
			t.Errorf("pista %d: dura %.1f s, MusicBrainz dice %.1f s", i+1, f.Duration, want.Length)
		}
		if f.Title != want.Title {
			t.Errorf("pista %d: título %q, esperaba %q", i+1, f.Title, want.Title)
		}
		t.Logf("%-40s %6.1f s  (mb %6.1f s)", f.Name, f.Duration, want.Length)
	}
	if scan.Cover == "" {
		t.Error("ScanFolder no ve cover.jpg")
	}

	// 7. Etiquetar de nuevo (idempotente: mismos nombres, sin errores).
	if m, err := app.TagFolder(dir, rel.Release.ID); err != nil || m != n {
		t.Errorf("TagFolder: n=%d err=%v", m, err)
	}
}

func TestNaturalLess(t *testing.T) {
	cases := [][2]string{{"2.mp3", "10.mp3"}, {"01 - a", "02 - b"}, {"Track 9", "Track 10"}, {"a", "b"}}
	for _, c := range cases {
		if !naturalLess(c[0], c[1]) {
			t.Errorf("%q debería ir antes que %q", c[0], c[1])
		}
		if naturalLess(c[1], c[0]) {
			t.Errorf("%q no debería ir antes que %q", c[1], c[0])
		}
	}
}

func TestBatch(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()

	// Disco entero sintético con el nombre típico de un vídeo de YouTube.
	res, err := app.SearchReleases("Cloudkicker", "Beacons")
	if err != nil || len(res.Results) == 0 {
		t.Fatal("búsqueda:", err)
	}
	var mbID string
	for _, r := range res.Results {
		if r.Source == srcMusicBrainz {
			mbID = r.ID
			break
		}
	}
	rel, err := app.GetRelease(mbID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Cloudkicker - Beacons (Full Album) [HD]")
	os.MkdirAll(dir, 0o755)
	long := filepath.Join(dir, "Cloudkicker - Beacons (Full Album) [HD].mp3")
	gen := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-t", ffTime(rel.TotalLength), "-c:a", "libmp3lame", "-q:a", "9", long)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generando audio: %v %s", err, out)
	}
	// Y una carpeta que debe ignorarse (sin audio) y otra que empieza por _.
	os.MkdirAll(filepath.Join(root, "sin audio"), 0o755)
	os.MkdirAll(filepath.Join(root, "_original"), 0o755)

	albums, err := app.ListAlbums(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 1 || albums[0].Files != 1 || albums[0].State != "pending" {
		t.Fatalf("ListAlbums: %+v", albums)
	}

	// Análisis: debe encontrar la edición sola, proponerla como separable
	// y enseñar las candidatas, sin confirmar nada.
	app.analyzeOne(ctx, dir)
	an := app.batch.get(dir)
	if an == nil || an.info.State != "splittable" || an.info.Confirmed || len(an.info.Candidates) == 0 {
		t.Fatalf("análisis: %+v", an.info)
	}
	t.Logf("análisis: %s (%d candidatas)", an.info.Message, len(an.info.Candidates))
	for _, c := range an.info.Candidates {
		t.Logf("  candidata: %s (%s, %s, %d pistas) evaluada=%v estado=%s",
			c.Release.Title, c.Release.Date, c.Release.Country, c.Release.TrackCount, c.Evaluated, c.State)
	}

	// Sin confirmar, aplicar no debe tocar nada.
	app.applyOne(ctx, dir, true, true, true)
	if countAudio(dir) != 1 {
		t.Fatal("se aplicó sin confirmación")
	}

	// Elegir por URL de MusicBrainz (como si viniera de Google) y confirmar.
	info, err := app.ChooseCandidate(dir, "https://musicbrainz.org/release/"+rel.Release.ID)
	if err != nil {
		t.Fatal("ChooseCandidate:", err)
	}
	dir = info.Dir // elegir una edición que cuadra renombra la carpeta
	t.Logf("carpeta tras elegir: %s", filepath.Base(dir))
	if an = app.batch.get(dir); an == nil || !an.info.Confirmed || an.info.State != "splittable" {
		t.Fatalf("tras elegir: %+v", an.info)
	}

	// Aplicar: separar (+ carátula). Debe quedar íntegro.
	app.applyOne(ctx, dir, true, true, true)
	an = app.batch.get(dir)
	if an.info.State != "ok" || an.info.Files != len(rel.Tracks) || !an.info.HasCover {
		t.Fatalf("aplicación: %+v", an.info)
	}
	t.Logf("aplicación: %s", an.info.Message)

	// Segundo análisis sobre las pistas ya separadas: íntegro directamente.
	app.analyzeOne(ctx, dir)
	if an = app.batch.get(dir); an.info.State != "ok" {
		t.Fatalf("reanálisis: %+v", an.info)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Cranial Dust [SWE] [Raw Black] 1996 - Ultimate Uprising (Full Demo)": "Cranial Dust 1996 - Ultimate Uprising",
		"Cloudkicker - Beacons (Full Album) [HD]":                             "Cloudkicker - Beacons",
		"Some Band - Some Album FULL ALBUM":                                   "Some Band - Some Album",
		"ZVERI":                                                               "ZVERI",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, quería %q", in, got, want)
		}
	}
}

func TestSources(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()

	// Deezer y Bandcamp directamente.
	dz, err := searchDeezer(ctx, "Cloudkicker", "Beacons")
	if err != nil || len(dz) == 0 {
		t.Fatal("deezer:", err, len(dz))
	}
	d, err := deezerDetail(ctx, parseKeyID(dz[0].ID))
	if err != nil || len(d.Tracks) < 10 || d.Release.CoverURL == "" {
		t.Fatalf("deezer detalle: %v, %d pistas, cover=%q", err, len(d.Tracks), d.Release.CoverURL)
	}
	t.Logf("deezer: %s - %s (%s) %d pistas, %s", d.Release.Artist, d.Release.Title, d.Release.Date, len(d.Tracks), fmtDur(d.TotalLength))

	bc, err := searchBandcamp(ctx, "Cloudkicker", "Beacons")
	if err != nil || len(bc) == 0 {
		t.Fatal("bandcamp:", err, len(bc))
	}
	b, err := bandcampDetail(ctx, parseKeyID(bc[0].ID))
	if err != nil || len(b.Tracks) < 10 || b.Release.CoverURL == "" {
		t.Fatalf("bandcamp detalle: %v, %d pistas, cover=%q", err, len(b.Tracks), b.Release.CoverURL)
	}
	t.Logf("bandcamp: %s - %s (%s) %d pistas, %s", b.Release.Artist, b.Release.Title, b.Release.Date, len(b.Tracks), fmtDur(b.TotalLength))

	// Búsqueda aproximada: errata en el título y acento distinto.
	res, err := app.SearchReleases("Cloudkiker", "Beacns")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res.Results {
		if similarity(r.Title, "Beacons") > 0.9 && similarity(r.Artist, "Cloudkicker") > 0.9 {
			found = true
			t.Logf("aproximada [%s]: %s - %s", r.Source, r.Artist, r.Title)
			break
		}
	}
	if !found {
		t.Errorf("la búsqueda aproximada no encontró Beacons entre %d resultados", len(res.Results))
	}
}

func parseKeyID(key string) string { _, id := parseKey(key); return id }

func TestBuildAlbum(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()

	res, err := app.SearchReleases("Cloudkicker", "Beacons")
	if err != nil {
		t.Fatal(err)
	}
	var mbID string
	for _, r := range res.Results {
		if r.Source == srcMusicBrainz && r.TrackCount == 11 {
			mbID = r.ID
			break
		}
	}
	rel, err := app.GetRelease(mbID)
	if err != nil {
		t.Fatal(err)
	}

	// Carpeta "mezcla" con dos pistas del disco, etiquetadas y con la
	// duración oficial, y nombres que no siguen ninguna convención.
	root := t.TempDir()
	src := filepath.Join(root, "mezcla")
	os.MkdirAll(src, 0o755)
	for i, name := range []string{"tema uno.mp3", "otro tema.mp3"} {
		tr := rel.Tracks[i+1] // pistas 2 y 3
		gen := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", ffTime(tr.Length),
			"-c:a", "libmp3lame", "-q:a", "9", "-metadata", "title="+tr.Title, "-metadata", "artist=Cloudkicker",
			"-metadata", "album=Beacons", filepath.Join(src, name))
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generando: %v %s", err, out)
		}
	}

	// Orígenes: los dos deben apuntar a Beacons.
	origins, err := app.Origins(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range origins {
		if o.State != "ok" || o.Release == nil || similarity(o.Release.Title, "Beacons") < 0.9 {
			t.Fatalf("origen: %+v", o)
		}
		t.Logf("origen: %s -> %s pista %d", o.Name, o.Release.Title, o.Track.Number)
	}

	// Crear el álbum moviendo los dos ficheros.
	b, err := app.BuildAlbum(src, mbID, []int{0, 1}, true)
	if err != nil {
		t.Fatal("BuildAlbum:", err)
	}
	t.Logf("álbum en %s: %d movidos, faltan %d", b.Dir, b.Moved, len(b.Missing))
	if b.Moved != 2 || len(b.Missing) != 9 || countAudio(src) != 0 {
		t.Fatalf("esperaba 2 movidos, 9 faltantes y la mezcla vacía: %+v, quedan %d", b, countAudio(src))
	}
	if !fileExists(filepath.Join(b.Dir, "cover.jpg")) {
		t.Error("sin cover.jpg en el álbum")
	}
	scan, _ := app.ScanFolder(b.Dir)
	for _, f := range scan.Files {
		t.Logf("  %s  title=%q track=%q cover=%v", f.Name, f.Title, f.Track, f.HasCover)
		if f.Title == "" || !f.HasCover {
			t.Errorf("%s sin curar", f.Name)
		}
	}

	// Completar: candidatos en YouTube para la pista 1 y descarga real.
	tr := b.Missing[0]
	cands, err := app.FindTrackSources("Cloudkicker", tr.Title, tr.Length)
	if err != nil || len(cands) == 0 {
		t.Fatalf("FindTrackSources: %v (%d)", err, len(cands))
	}
	for i, c := range cands {
		if i < 3 {
			t.Logf("  candidato: %s [%s] %s (%+.0f s, sim %.2f)", c.Title, c.Channel, fmtDur(c.Duration), c.Diff, c.Sim)
		}
	}
	out, err := app.DownloadTrack(cands[0].URL, b.Dir, mbID, tr.Number)
	if err != nil {
		t.Fatal("DownloadTrack:", err)
	}
	f, _ := probe(ctx, out)
	t.Logf("descargada: %s  title=%q track=%q cover=%v dur=%s", filepath.Base(out), f.Title, f.Track, f.HasCover, fmtDur(f.Duration))
	if f.Title != tr.Title || !f.HasCover {
		t.Errorf("la pista descargada no está curada: %+v", f)
	}
	missing, _ := app.MissingTracks(b.Dir, mbID)
	if len(missing) != 8 {
		t.Errorf("tras descargar deberían faltar 8, faltan %d", len(missing))
	}
}

func TestRenameToEdition(t *testing.T) {
	app := testApp(t)
	rel := mbRelease{Artist: "Frost", Title: "Under the Hungarian Blackmoon", Date: "1998-05-01"}
	root := t.TempDir()
	dir := filepath.Join(root, "1998 - Under the Hungarian Blackmoon (Demo)")
	os.MkdirAll(dir, 0o755)

	got, err := app.renameToEdition(dir, rel)
	if err != nil || filepath.Base(got) != "Frost - Under the Hungarian Blackmoon (1998)" || !fileExists(got) || fileExists(dir) {
		t.Fatalf("renombrado: %q, %v", got, err)
	}
	// Ya se llama así: no hace nada.
	if again, err := app.renameToEdition(got, rel); err != nil || again != got {
		t.Fatalf("segunda vez: %q, %v", again, err)
	}
	// Otra carpeta con el mismo destino: no se mezcla.
	other := filepath.Join(root, "otra")
	os.MkdirAll(other, 0o755)
	if same, err := app.renameToEdition(other, rel); err == nil || same != other {
		t.Fatalf("debería negarse a mezclar: %q, %v", same, err)
	}
}

func TestDuplicates(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()
	res, _ := app.SearchReleases("Cloudkicker", "Beacons")
	var mbID string
	for _, r := range res.Results {
		if r.Source == srcMusicBrainz && r.TrackCount == 11 {
			mbID = r.ID
			break
		}
	}
	rel, err := app.GetRelease(mbID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	gen := func(dir, name string, tr mbTrack, q string) {
		os.MkdirAll(dir, 0o755)
		cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", ffTime(tr.Length),
			"-c:a", "libmp3lame", "-q:a", q, "-metadata", "title="+tr.Title, "-metadata", "artist=Cloudkicker",
			"-metadata", "album=Beacons", filepath.Join(dir, name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generando: %v %s", err, out)
		}
	}
	// Copia A: completa (11 pistas) a baja calidad, nombre de YouTube.
	a := filepath.Join(root, "Cloudkicker - Beacons (Full Album) [HD]")
	for i, tr := range rel.Tracks {
		gen(a, fmt.Sprintf("%02d - %s.mp3", i+1, safeName(tr.Title)), tr, "9")
	}
	// Copia B: solo 4 pistas pero mejor calidad, nombre canónico.
	b := filepath.Join(root, "Cloudkicker - Beacons (2010)")
	for i, tr := range rel.Tracks[:4] {
		gen(b, fmt.Sprintf("%02d - %s.mp3", i+1, safeName(tr.Title)), tr, "2")
	}
	// Y un disco distinto que no debe agruparse.
	gen(filepath.Join(root, "Otro grupo - Otro disco"), "01 - x.mp3", mbTrack{Title: "x", Length: 30}, "9")

	groups, err := app.FindDuplicates(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Folders) != 2 {
		t.Fatalf("esperaba 1 grupo de 2: %+v", groups)
	}
	g := groups[0]
	t.Logf("grupo «%s» (%s): %d pistas distintas, edición %v", g.Album, g.Artist, g.Union, g.Release != nil)
	for _, f := range g.Folders {
		t.Logf("  %-45s %2d pistas %3dk carátula=%v etiquetas=%d%% canónico=%v estado=%s recomendada=%v", f.Name, f.Files, f.Bitrate, f.Cover, f.Tagged, f.Canonical, f.State, f.Recommend)
	}
	keep := ""
	for _, f := range g.Folders {
		if f.Recommend {
			keep = f.Dir
		}
	}
	if filepath.Base(keep) != "Cloudkicker - Beacons (Full Album) [HD]" {
		t.Fatalf("debería recomendar la copia completa, recomendó %s", keep)
	}

	// Fusionar al revés a propósito: conservar la canónica (B) y traer las 7
	// pistas que le faltan de A; A se aparta.
	m, err := app.MergeDuplicates(b, []string{a})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fusión: %d traídas, apartadas %v", m.Moved, m.Archived)
	if m.Moved != 7 || countAudio(b) != 11 || fileExists(a) || len(m.Archived) != 1 {
		t.Fatalf("fusión incorrecta: %+v, en B hay %d, A existe=%v", m, countAudio(b), fileExists(a))
	}
}

// La carátula sale de otra fuente cuando la de la edición no la tiene:
// una edición "de MusicBrainz" con un ID sin arte en Cover Art Archive.
func TestCoverFallback(t *testing.T) {
	ctx := context.Background()
	d := mbReleaseDetail{Release: mbRelease{
		ID: "00000000-0000-4000-8000-000000000000", Source: srcMusicBrainz, Title: "Beacons", Artist: "Cloudkicker",
	}}
	d.Tracks = make([]mbTrack, 11)
	dest := filepath.Join(t.TempDir(), "cover.jpg")
	src, err := coverFor(ctx, d, dest, searchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Size() < 5000 {
		t.Fatalf("carátula rara: %v (%d bytes)", err, info.Size())
	}
	t.Logf("carátula de %s, %d bytes", src, info.Size())
	if src == srcMusicBrainz || src == "Cover Art Archive" {
		t.Errorf("debía venir de otra fuente, no de %s", src)
	}
}

// Separar sin duraciones: detectar los silencios y cortar por marcas.
func TestSplitBySilence(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()
	dir := t.TempDir()
	long := filepath.Join(dir, "Demo (Full).mp3")
	// Tres "canciones" de 30, 40 y 35 s con 2 s de silencio entre ellas.
	gen := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
		"-filter_complex", "sine=frequency=440:duration=30[a];anullsrc=r=44100:cl=mono:d=2[s1];sine=frequency=660:duration=40[b];anullsrc=r=44100:cl=mono:d=2[s2];sine=frequency=880:duration=35[c];[a][s1][b][s2][c]concat=n=5:v=0:a=1[out]",
		"-map", "[out]", "-c:a", "libmp3lame", "-q:a", "9", long)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generando audio: %v %s", err, out)
	}
	marks, err := app.DetectSplits(long, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("marcas: %v", marks)
	if len(marks) != 2 || math.Abs(marks[0]-32) > 1.5 || math.Abs(marks[1]-74) > 1.5 {
		t.Fatalf("marcas esperadas ~32 y ~74: %v", marks)
	}
	rel := mbReleaseDetail{Release: mbRelease{Title: "Demo", Artist: "Prueba"}, Tracks: []mbTrack{
		{Number: 1, Title: "Uno"}, {Number: 2, Title: "Dos"}, {Number: 3, Title: "Tres"},
	}}
	outs, err := app.splitMarks(long, rel, marks)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 3 {
		t.Fatalf("salidas: %v", outs)
	}
	for i, want := range []float64{32, 42, 35} {
		tr, err := probe(ctx, outs[i])
		if err != nil || math.Abs(tr.Duration-want) > 1.5 || tr.Title == "" {
			t.Errorf("%s: %v dura %.1f (esperaba ~%.0f) título %q", filepath.Base(outs[i]), err, tr.Duration, want, tr.Title)
		}
	}
	if fileExists(long) || !fileExists(filepath.Join(dir, "_original", "Demo (Full).mp3")) {
		t.Error("el original debe apartarse a _original")
	}
}

// Una edición sin duraciones (como las de Discogs) las toma de otra fuente
// que tenga el mismo disco.
func TestFillLengths(t *testing.T) {
	app := testApp(t)
	d := mbReleaseDetail{Release: mbRelease{ID: "discogs:0", Source: srcDiscogs, Title: "Chantant Le Chant Du Diable", Artist: "Fantôme Oublié"},
		Tracks: []mbTrack{{Number: 1, Title: "Introduction"}, {Number: 2, Title: "Mille Ans De Vengeance"}, {Number: 3, Title: "Une Bannière, Une Épée"}, {Number: 4, Title: "Strasbourg 1349"}, {Number: 5, Title: "Violence Nocturne"}}}
	app.fillLengths(context.Background(), &d)
	t.Logf("duraciones de %q: total %s", d.Release.LengthsFrom, fmtDur(d.TotalLength))
	for _, tr := range d.Tracks {
		t.Logf("  %d %s %s", tr.Number, tr.Title, fmtDur(tr.Length))
	}
	if d.TotalLength == 0 {
		t.Skip("ninguna fuente tiene este disco con duraciones ahora mismo")
	}
	if d.Release.LengthsFrom == "" || d.Tracks[1].Length == 0 {
		t.Errorf("duraciones incompletas: %+v", d)
	}
}

// Bandcamp como fuente de pistas: la página del álbum da la URL de cada
// pista, y una edición de otra fuente se casa con el disco de Bandcamp.
func TestBandcampTrackSources(t *testing.T) {
	app := testApp(t)
	missing := []mbTrack{{Number: 1, Title: "We Are Going to Invert"}, {Number: 2, Title: "Here, Wait a Minute! Damn It!"}, {Number: 9, Title: "Una pista que no existe"}}
	found, err := app.FindAlbumSourcesBandcamp("00000000-0000-4000-8000-000000000000", "Cloudkicker", "Beacons", missing)
	if err != nil {
		t.Fatal(err)
	}
	for n, cands := range found {
		for _, c := range cands {
			t.Logf("pista %d: %s (%s) %s", n, c.Title, fmtDur(c.Duration), c.URL)
		}
	}
	if len(found[1]) == 0 || len(found[2]) == 0 || !strings.Contains(found[2][0].URL, "bandcamp.com/track/") {
		t.Errorf("esperaba la URL de la pista en Bandcamp: %+v", found)
	}
	if len(found[9]) != 0 {
		t.Errorf("una pista que no está no debe tener candidato: %+v", found[9])
	}
}

// Una pista de Bandcamp se baja y se cura como cualquier vídeo.
func TestDownloadBandcampTrack(t *testing.T) {
	app := testApp(t)
	dir := t.TempDir()
	key := sourceKey(srcBandcamp, "https://cloudkicker.bandcamp.com/album/beacons")
	out, err := app.DownloadTrack("https://cloudkicker.bandcamp.com/track/we-are-going-to-invert", dir, key, 1)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := probe(context.Background(), out)
	if err != nil || tr.Duration < 35 || tr.Duration > 50 || tr.Title == "" || tr.Track == "" {
		t.Fatalf("%s: %v %+v", filepath.Base(out), err, tr)
	}
	t.Logf("%s: %s, %q, pista %s, %d kbps", filepath.Base(out), fmtDur(tr.Duration), tr.Title, tr.Track, tr.Bitrate)
}

// Convertir de formato: un WMA (que la ventana no reproduce) pasa a MP3
// conservando etiquetas y carátula; el original se aparta.
func TestConvertTrack(t *testing.T) {
	app := testApp(t)
	ctx := context.Background()
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.jpg")
	f, _ := os.Create(cover)
	jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 40)), nil)
	f.Close()
	src := filepath.Join(dir, "01 - Prueba.wma")
	gen := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "2",
		"-metadata", "title=Prueba", "-metadata", "artist=Grupo", "-c:a", "wmav2", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generando wma: %v %s", err, out)
	}
	// Con carátula incrustada de por medio: primero a FLAC con imagen.
	flacPath, err := app.ConvertTrack(src, "flac")
	if err != nil {
		t.Fatal(err)
	}
	if err := rewrite(ctx, flacPath, flacPath, nil, cover); err != nil {
		t.Fatal(err)
	}
	mp3, err := app.ConvertTrack(flacPath, "mp3-320")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := probe(ctx, mp3)
	if err != nil || tr.Title != "Prueba" || tr.Artist != "Grupo" || tr.Codec != "mp3" || !tr.HasCover || tr.Duration < 1.5 {
		t.Fatalf("mp3: %v %+v", err, tr)
	}
	if fileExists(flacPath) || !fileExists(filepath.Join(dir, "_original", "01 - Prueba.flac")) || !fileExists(filepath.Join(dir, "_original", "01 - Prueba.wma")) {
		t.Error("los originales deben apartarse a _original")
	}
	if _, err := app.ConvertTrack(mp3, "mp3-v0"); err == nil {
		t.Error("convertir mp3 a mp3 debe negarse")
	}
	t.Logf("%s: %s %d kbps, carátula %v", filepath.Base(mp3), tr.Codec, tr.Bitrate, tr.HasCover)
}
