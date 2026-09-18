package main

import (
	"math"
	"testing"
)

// Tests unitarios (sin red ni herramientas): `go test ./...`.

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		min  float64 // parecido mínimo esperado
		max  float64
	}{
		{"Flögo de bort", "Flogo De Bort", 1, 1},                           // acentos y mayúsculas
		{"Endzeit Barbarossa", "1941 - Endzeit Barbarossa (2021)", 0.6, 1}, // contenido
		{"Black Demise", "Black Demize", 0.85, 1},                          // errata
		{"Aria of Vernal Tombs", "Tombs Vernal of Aria", 0.9, 1},           // reordenado (Jaccard)
		{"Ave Crux Alba", "Cranial Dust", 0, 0.35},                         // nada que ver
		{"Rock & Roll", "Rock and Roll", 1, 1},                             // & vs and
	}
	for _, c := range cases {
		got := similarity(c.a, c.b)
		if got < c.min || got > c.max {
			t.Errorf("similarity(%q, %q) = %.2f, esperaba entre %.2f y %.2f", c.a, c.b, got, c.min, c.max)
		}
	}
}

func TestTrackTitleOf(t *testing.T) {
	cases := map[localTrack]string{
		{Name: "01 - This Frozen Night.mp3"}:                         "This Frozen Night",
		{Name: "3. Ultimate Uprising.mp3"}:                           "Ultimate Uprising",
		{Name: "Ave Crux Alba - Afterlife.mp3"}:                      "Afterlife",
		{Name: "Cranial Dust [SWE] 1996 - Uprising (Full Demo).mp3"}: "Uprising",
		{Name: "x.mp3", Title: "Rusalka"}:                            "Rusalka",
	}
	for f, want := range cases {
		if got := trackTitleOf(f); got != want {
			t.Errorf("trackTitleOf(%q/%q) = %q, esperaba %q", f.Name, f.Title, got, want)
		}
	}
}

func TestMatchTracksReordered(t *testing.T) {
	// Ficheros desordenados y con nombres distintos a los oficiales.
	files := []localTrack{
		{Name: "b.mp3", Title: "Mraku Viditu", Duration: 300},
		{Name: "a.mp3", Title: "Zveri (intro)", Duration: 120},
		{Name: "c.mp3", Title: "Rusalka", Duration: 240},
		{Name: "extra.mp3", Title: "Bonus live", Duration: 500},
	}
	tracks := []mbTrack{
		{Number: 1, Title: "Zveri", Length: 121},
		{Number: 2, Title: "Rusalka", Length: 239},
		{Number: 3, Title: "Mraku Viditu", Length: 301},
	}
	pairs := matchTracks(files, tracks)
	want := []int{1, 2, 0}
	for i, p := range pairs {
		if p.File != want[i] {
			t.Errorf("pista %d emparejada con fichero %d, esperaba %d", i+1, p.File, want[i])
		}
	}
	if assigned, matched := countMatched(pairs); assigned != 3 || matched != 3 {
		t.Errorf("assigned=%d matched=%d, esperaba 3/3", assigned, matched)
	}
}

func TestMatchTracksWrongDuration(t *testing.T) {
	// Mismo título pero duración muy distinta: se empareja (para poder
	// etiquetar) pero no cuenta como "cuadra".
	files := []localTrack{{Name: "01 - Intro.mp3", Duration: 400}}
	tracks := []mbTrack{{Number: 1, Title: "Intro", Length: 100}}
	pairs := matchTracks(files, tracks)
	if pairs[0].File != 0 {
		t.Fatal("debería emparejarse por título")
	}
	if _, matched := countMatched(pairs); matched != 0 {
		t.Error("no debería contar como cuadrada")
	}
}

func TestGuessNames(t *testing.T) {
	frost := []localTrack{{Artist: "Frost", Album: "Under the Hungarian Blackmoon"}, {Artist: "Frost", Album: "Under the Hungarian Blackmoon"}}
	cases := []struct {
		folder        string
		files         []localTrack
		artist, album string
	}{
		{"1998 - Under the Hungarian Blackmoon (Demo)", frost, "Frost", "Under the Hungarian Blackmoon"},
		{"Under the Hungarian Blackmoon - Frost", frost, "Frost", "Under the Hungarian Blackmoon"},
		{"Frost - Under the Hungarian Blackmoon", frost, "Frost", "Under the Hungarian Blackmoon"},
		{"2015. Aria of Vernal Tombs", []localTrack{{Artist: "Obsequiae"}}, "Obsequiae", "Aria of Vernal Tombs"},
		{"ZVERI", []localTrack{{Artist: "Ravn"}}, "Ravn", "ZVERI"},
		{"Cloudkicker - Beacons (Full Album) [HD]", nil, "Cloudkicker", "Beacons"},
		{"1941 - Endzeit Barbarossa (2021)", nil, "", "Endzeit Barbarossa"},
		{"Oliphant - Oliphant - Songs from the Crusades", nil, "Oliphant", "Songs from the Crusades"},
		{"Oliphant - Oliphant - Songs from the Crusades", []localTrack{{Artist: "Oliphant"}}, "Oliphant", "Songs from the Crusades"},
	}
	for _, c := range cases {
		artist, album := guessNames(c.folder, c.files)
		if artist != c.artist || album != c.album {
			t.Errorf("guessNames(%q) = (%q, %q), esperaba (%q, %q)", c.folder, artist, album, c.artist, c.album)
		}
	}
}

func TestAlbumFolderName(t *testing.T) {
	cases := []struct {
		rel  mbRelease
		want string
	}{
		{mbRelease{Artist: "Frost", Title: "Under the Hungarian Blackmoon", Date: "1998-05-01"}, "Frost - Under the Hungarian Blackmoon (1998)"},
		{mbRelease{Artist: "Oliphant", Title: "Oliphant - Songs from the Crusades", Date: "2000"}, "Oliphant - Songs from the Crusades (2000)"},
		{mbRelease{Artist: "Joglaresa", Title: "Magdalena: Medieval Songs for Mary Magdalen", Date: "2004"}, "Joglaresa - Magdalena - Medieval Songs for Mary Magdalen (2004)"},
		{mbRelease{Artist: "Ensemble Tre Fontane", Title: "1992 - Le Chant des troubadours, volume 1"}, "Ensemble Tre Fontane - Le Chant des troubadours, volume 1 (1992)"},
		{mbRelease{Artist: "", Title: "Various / Anthology"}, "Various - Anthology"},
	}
	for _, c := range cases {
		if got := albumFolderName(c.rel); got != c.want {
			t.Errorf("albumFolderName(%q, %q) = %q, esperaba %q", c.rel.Artist, c.rel.Title, got, c.want)
		}
	}
}

func TestMatchSoulseekCandidates(t *testing.T) {
	missing := []mbTrack{
		{Number: 1, Title: "We are going to invert…", Length: 41},
		{Number: 2, Title: "Here, wait a minute! Damn it!", Length: 107},
		{Number: 3, Title: "Oh, god.", Length: 340},
	}
	res := slskSearch{Responses: []slskResponse{
		{Username: "lento", FreeSlot: false, QueueLength: 9, Files: []slskFile{
			{Filename: `@@a\Cloudkicker\Beacons\01 We Are Going To Invert.mp3`, Name: "01 We Are Going To Invert.mp3", Folder: "Beacons", Size: 1e6, BitRate: 320, Length: 41},
			{Filename: `@@a\Cloudkicker\Beacons\02 Here, Wait a Minute! Damn It!.mp3`, Name: "02 Here, Wait a Minute! Damn It!.mp3", Folder: "Beacons", Size: 3e6, BitRate: 320, Length: 107},
		}},
		{Username: "rapido", FreeSlot: true, QueueLength: 0, Files: []slskFile{
			{Filename: `@@b\Cloudkicker - Beacons (2010) [FLAC]\01 - We are going to invert....flac`, Name: "01 - We are going to invert....flac", Folder: "Cloudkicker - Beacons (2010) [FLAC]", Size: 8e6, Length: 41},
			{Filename: `@@b\otro\Oh, god (live bootleg).mp3`, Name: "Oh, god (live bootleg).mp3", Folder: "otro", Size: 5e6, BitRate: 128, Length: 402},
			{Filename: `@@b\x\cover.jpg`, Name: "cover.jpg", Folder: "x", Size: 1e5},
		}},
	}}
	got := matchSoulseekCandidates(res, "Beacons", missing)
	// Pista 1: el FLAC con hueco libre debe ir primero.
	if c := got[1]; len(c) != 2 || c[0].Username != "rapido" || c[0].Ext != "flac" {
		t.Fatalf("pista 1: %+v", c)
	}
	// Pista 2: solo el usuario lento la tiene.
	if c := got[2]; len(c) != 1 || c[0].Username != "lento" || c[0].BitRate != 320 {
		t.Fatalf("pista 2: %+v", c)
	}
	// Pista 3: el bootleg dura 62 s más; se ofrece (título parecido) pero con Δ grande.
	if c := got[3]; len(c) != 1 || math.Abs(c[0].Diff-62) > 0.1 {
		t.Fatalf("pista 3: %+v", c)
	}
}

func TestIsUpgrade(t *testing.T) {
	mp3128 := localTrack{Codec: "mp3", Bitrate: 128}
	mp3320 := localTrack{Codec: "mp3", Bitrate: 320}
	flac := localTrack{Codec: "flac", Bitrate: 900}
	cases := []struct {
		local localTrack
		cand  trackSource
		want  bool
	}{
		{mp3128, trackSource{Ext: "flac"}, true},
		{mp3128, trackSource{Ext: "mp3", BitRate: 320}, true},
		{mp3128, trackSource{Ext: "mp3", BitRate: 160}, false}, // salto pequeño
		{mp3320, trackSource{Ext: "mp3", BitRate: 320}, false},
		{mp3320, trackSource{Ext: "flac"}, true},
		{flac, trackSource{Ext: "flac"}, false},
		{flac, trackSource{Ext: "mp3", BitRate: 320}, false},
		{mp3128, trackSource{Ext: "mp3", BitRate: 0}, false}, // bitrate desconocido: no se arriesga
	}
	for _, c := range cases {
		if got := isUpgrade(c.local, c.cand); got != c.want {
			t.Errorf("isUpgrade(%s %dk -> %s %dk) = %v, esperaba %v", c.local.Codec, c.local.Bitrate, c.cand.Ext, c.cand.BitRate, got, c.want)
		}
	}
}

// Un solo fichero: separable si dura lo que el disco, pista suelta si es una
// de las pistas (duración y título), y no cuadra en otro caso.
func TestEvaluateSingle(t *testing.T) {
	rel := &mbReleaseDetail{Tracks: []mbTrack{
		{Number: 1, Title: "Ma joie me semont", Length: 240},
		{Number: 2, Title: "L'amours dont sui espris", Length: 495},
		{Number: 3, Title: "Quant je plus sui", Length: 310},
	}}
	rel.TotalLength = 1045
	one := func(name string, dur float64) folderScan {
		return folderScan{Files: []localTrack{{Name: name, Duration: dur}}, Total: dur}
	}
	cases := []struct {
		name  string
		scan  folderScan
		state string
	}{
		{"pista suelta", one("Blondel de Nesle - L'amours dont sui espris.mp3", 496), "single"},
		{"suelta sin título pero misma duración", one("track.mp3", 495.5), "single"},
		{"disco entero", one("Blondel de Nesle - Full Album.mp3", 1044), "splittable"},
		{"duración que no es de nadie", one("Cosa.mp3", 60), "mismatch"},
		{"título parecido, duración lejana", one("L'amours dont sui espris.mp3", 470), "mismatch"},
	}
	for _, c := range cases {
		if st, _, _ := evaluate(rel, c.scan); st != c.state {
			t.Errorf("%s: estado %q, esperaba %q", c.name, st, c.state)
		}
	}
	if !identified("single") || fits("single") {
		t.Error("single debe contar como identificado pero no como encaje (no se renombra)")
	}
}

// Dos carpetas del mismo disco con el artista repetido delante o el año
// detrás deben agruparse como duplicados.
func TestSameAlbum(t *testing.T) {
	cases := []struct {
		artistA, albumA, artistB, albumB string
		want                             bool
	}{
		{"Oliphant", "Oliphant - Songs from the Crusades", "Oliphant", "Songs from the Crusades", true},
		{"Oliphant", "Songs from the Crusades (1996)", "Oliphant", "Songs from the Crusades", true},
		{"", "Oliphant - Songs from the Crusades", "Oliphant", "Songs from the Crusades", true},
		{"Oliphant", "Oliphant: Songs from the Crusades", "", "Songs from the Crusades", true},
		{"Frost", "Under the Hungarian Blackmoon", "Frost", "Under the Hungarian Blackmoon (Demo)", true},
		{"Oliphant", "Songs from the Crusades", "Oliphant", "Songs of the Troubadours", false},
		{"Oliphant", "Songs from the Crusades", "Sequentia", "Songs from the Crusades", false},
	}
	for _, c := range cases {
		if got := sameAlbum(c.artistA, c.albumA, c.artistB, c.albumB); got != c.want {
			t.Errorf("sameAlbum(%q/%q, %q/%q) = %v, esperaba %v", c.artistA, c.albumA, c.artistB, c.albumB, got, c.want)
		}
	}
	if got := stripArtistPrefix("Oliphant - Songs", "Sequentia"); got != "Oliphant - Songs" {
		t.Errorf("no debe quitar un prefijo que no es el artista: %q", got)
	}
}

// Duraciones de otra fuente: por título, y por posición si hay las mismas
// pistas y algún título no casa.
func TestMatchLengths(t *testing.T) {
	mine := []mbTrack{{Title: "Introduction"}, {Title: "Mille Ans De Vengeance"}, {Title: "Une Bannière, Une Épée"}}
	other := []mbTrack{{Title: "Intro", Length: 60}, {Title: "Mille ans de vengeance", Length: 300}, {Title: "Une banniere, une epee", Length: 240}}
	if got := matchLengths(mine, other); len(got) != 3 || got[0] != 60 || got[1] != 300 || got[2] != 240 {
		t.Errorf("mismo tracklist: %v", got)
	}
	// Desordenado en la otra fuente: por título.
	shuffled := []mbTrack{other[2], other[0], other[1]}
	if got := matchLengths(mine, shuffled); len(got) != 3 || got[1] != 300 || got[2] != 240 {
		t.Errorf("desordenado: %v", got)
	}
	// Otro disco con el mismo número de pistas: ningún título casa -> nada.
	alien := []mbTrack{{Title: "Alpha", Length: 1}, {Title: "Beta", Length: 2}, {Title: "Gamma", Length: 3}}
	if got := matchLengths(mine, alien); got != nil {
		t.Errorf("otro disco no debe dar duraciones: %v", got)
	}
	// Distinto número de pistas y solo una casa: nada.
	if got := matchLengths(mine, other[:2]); got != nil {
		t.Errorf("faltan pistas: %v", got)
	}
}
