package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestObscurity(t *testing.T) {
	// Cuanto más conocido, menos premio; sin datos, ni arriba ni abajo.
	prev := 2.0
	for _, n := range []int{0, 1000, 5000, 20000, 85000, 500000, 2000000} {
		o := obscurity(n)
		if o >= prev || o <= 0 {
			t.Errorf("obscurity(%d) = %.2f, no baja de %.2f", n, o, prev)
		}
		prev = o
	}
	if o := obscurity(20000); o < 0.49 || o > 0.51 {
		t.Errorf("obscurity(20000) = %.2f, quería 0,5", o)
	}
	if obscurity(-1) != 0.8 {
		t.Error("desconocido para Last.fm: 0,8")
	}
}

// tasteApp: una colección de mentira (ColecciónArtistaDisco) con una caché
// propia, para no tocar favoritos ni escuchas de verdad.
func tasteApp(t *testing.T) *App {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	root := t.TempDir()
	disc := func(artist, album string, age time.Duration) string {
		d := filepath.Join(root, artist, album)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "01 - x.mp3"), []byte("x"), 0o644)
		old := time.Now().Add(-age)
		os.Chtimes(d, old, old)
		return d
	}
	year := 365 * 24 * time.Hour
	for i := 0; i < 6; i++ { // muchos discos de alguien que no se escucha
		disc("Montones", "Disco "+string(rune('A'+i)), year)
	}
	fav := disc("Querido", "El bueno", year)
	disc("Escuchado", "Uno", year)
	disc("Nuevo", "Recien", 24*time.Hour)
	for i := 0; i < 4; i++ {
		disc("Relleno"+string(rune('A'+i)), "X", year)
	}
	a := &App{ctx: context.Background(), emit: func(string, ...any) {}, settings: settings{OutDir: root}}
	plays.loaded, favs.loaded, favs.dirs, dismissed.loaded = false, false, nil, false
	ownedCache.at = time.Time{}
	t.Cleanup(func() {
		plays.loaded, favs.loaded, favs.dirs, dismissed.loaded = false, false, nil, false
		ownedCache.at = time.Time{}
	})
	if _, err := a.SetFavorite(fav, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		countPlay("Escuchado")
	}
	return a
}

func TestTasteSeeds(t *testing.T) {
	a := tasteApp(t)
	seeds := a.tasteSeeds(context.Background(), "")
	order := map[string]int{}
	why := map[string][]string{}
	for i, s := range seeds {
		order[s.Name] = i
		why[s.Name] = s.Why
	}
	// El favorito y el escuchado van por delante del que solo tiene muchos discos.
	if !(order["Querido"] < order["Montones"] && order["Escuchado"] < order["Montones"]) {
		t.Errorf("orden: %+v", seeds)
	}
	if order["Nuevo"] > order["Montones"] {
		t.Errorf("lo reciente debería pesar más que tener muchos discos: %+v", seeds)
	}
	if w := why["Querido"]; len(w) != 1 || w[0] != "favorito" {
		t.Errorf("por qué Querido: %v", w)
	}
	if w := why["Montones"]; len(w) != 1 || w[0] != "colección" {
		t.Errorf("por qué Montones: %v", w)
	}
}

func TestTasteRecentIgnoredWhenEverythingIsRecent(t *testing.T) {
	a := tasteApp(t)
	// Todo tocado ayer (renombrado, etiquetado…): «reciente» ya no dice nada.
	now := time.Now()
	for _, o := range a.ownedAlbums() {
		os.Chtimes(o.dir, now, now)
	}
	for _, s := range a.tasteSeeds(context.Background(), "") {
		for _, w := range s.Why {
			if w == "reciente" {
				t.Errorf("%s sale como reciente", s.Name)
			}
		}
	}
}

func TestDismissArtist(t *testing.T) {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	dismissed.loaded = false
	t.Cleanup(func() { dismissed.loaded = false })
	a := &App{}
	if err := a.DismissArtist("Satanic Warmaster", false); err != nil {
		t.Fatal(err)
	}
	dismissed.loaded = false // se vuelve a leer de disco
	if !isDismissed("satanic warmaster") || isDismissed("Horna") {
		t.Error("descartado mal guardado")
	}
	a.DismissArtist("Satanic Warmaster", true)
	if isDismissed("Satanic Warmaster") {
		t.Error("deshacer no lo ha quitado")
	}
}
