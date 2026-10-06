package main

import (
	"context"
	"testing"
)

func TestRadioPick(t *testing.T) {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	dismissed.loaded = false
	radio.played = map[string]bool{}
	t.Cleanup(func() { dismissed.loaded = false; radio.played = map[string]bool{} })
	a := &App{}
	a.DismissArtist("Descartado", false)
	pool := []discArtist{{Name: "Uno", Match: 1}, {Name: "Dos", Match: 0.5}, {Name: "Descartado", Match: 1}, {Name: "Tres", Match: 0.1}}

	// Una tanda: cada artista una vez, nunca el descartado.
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		ar, ok := radioPick(pool)
		if !ok || seen[ar.Name] || ar.Name == "Descartado" {
			t.Fatalf("vuelta %d: %q (ya visto %v)", i, ar.Name, seen)
		}
		seen[ar.Name] = true
	}
	// Agotados: empieza otra tanda en vez de quedarse sin nada.
	if ar, ok := radioPick(pool); !ok || ar.Name == "Descartado" {
		t.Fatalf("otra tanda: %q %v", ar.Name, ok)
	}
	if _, ok := radioPick([]discArtist{{Name: "Descartado"}}); ok {
		t.Error("solo hay descartados: no debería elegir nada")
	}
}

func TestRadioLikes(t *testing.T) {
	t.Setenv("MUSIC_PICKER_CACHE", t.TempDir())
	likes.loaded, likes.list = false, nil
	t.Cleanup(func() { likes.loaded, likes.list = false, nil })
	a := &App{}
	tr := radioTrack{Artist: "Serpent's Isle", Title: "The Empire's Fleet Returns", Album: "A Lost Aquatic Kingdom", AlbumKey: "bandcamp:x"}
	a.RadioLike(tr)
	a.RadioLike(tr) // dos veces la misma: una
	got, err := a.RadioLike(radioTrack{Artist: "Hyver", Title: "Bois Sauvage"})
	if err != nil || len(got) != 2 || got[0].Artist != "Hyver" || got[1].AlbumKey != "bandcamp:x" {
		t.Fatalf("lista: %+v %v", got, err)
	}
	likes.loaded, likes.list = false, nil // de disco
	if n := likedArtists()["Serpent's Isle"]; n != 1 {
		t.Errorf("likedArtists = %d", n)
	}
	got, _ = a.RadioUnlike("hyver", "bois sauvage")
	if len(got) != 1 || got[0].Artist != "Serpent's Isle" {
		t.Errorf("tras quitar: %+v", got)
	}
}

// Lo marcado con ♥ en la radio cuenta para el gusto, aunque el artista no
// esté en la colección.
func TestTasteCountsRadioLikes(t *testing.T) {
	a := tasteApp(t)
	likes.loaded, likes.list = false, nil
	t.Cleanup(func() { likes.loaded, likes.list = false, nil })
	a.RadioLike(radioTrack{Artist: "Nadie Lo Tiene", Title: "Una"})
	for _, s := range a.tasteSeeds(context.Background(), "") {
		if s.Name == "Nadie Lo Tiene" {
			if len(s.Why) != 1 || s.Why[0] != "me gusta" || s.Weight < 2 {
				t.Errorf("semilla: %+v", s)
			}
			return
		}
	}
	t.Error("el artista con ♥ no sale como semilla")
}
