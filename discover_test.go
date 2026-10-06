package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameDiscTitle(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Plaguewielder", "Plagueweilder", true},                             // errata de Deezer
		{"Sempiternal Past: The Darkthrone Demos", "Sempiternal Past", true}, // subtítulo
		{"Goatlord", "Goatlord (Original)", true},
		{"Panzerfaust", "Total Death", false},
		{"Live 1", "Live 2", false},
	}
	for _, c := range cases {
		if got := sameDiscTitle(c.a, c.b); got != c.want {
			t.Errorf("sameDiscTitle(%q, %q) = %v, quería %v", c.a, c.b, got, c.want)
		}
	}
}

func TestMergeDiscographies(t *testing.T) {
	rel := func(src, title, kind, date, cover string) discRelease {
		return discRelease{mbRelease: mbRelease{ID: src + title, Source: src, Title: title, Date: date, CoverURL: cover}, Kind: kind}
	}
	mbz := []discRelease{
		rel(srcMusicBrainz, "Transilvanian Hunger", "album", "1994-02-17", ""),
		rel(srcMusicBrainz, "Thulcandra", "ep", "1989", ""),
		rel(srcMusicBrainz, "Soulside Journey", "album", "1991-01-13", ""),
	}
	dz := []discRelease{
		rel(srcDeezer, "Transilvanian Hunger (Remastered)", "album", "2010-01-01", "http://dz/th.jpg"),
		rel(srcDeezer, "Old Star", "album", "2019-05-31", "http://dz/os.jpg"),
	}
	got := mergeDiscographies(mbz, dz)
	var titles []string
	for _, r := range got {
		titles = append(titles, r.Title)
	}
	want := []string{"Soulside Journey", "Transilvanian Hunger", "Old Star", "Thulcandra"}
	if len(titles) != len(want) {
		t.Fatalf("títulos = %v, quería %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("títulos = %v, quería %v", titles, want)
		}
	}
	// El de MusicBrainz se queda con la carátula de Deezer.
	if got[1].Source != srcMusicBrainz || got[1].CoverURL != "http://dz/th.jpg" {
		t.Errorf("Transilvanian Hunger = %+v", got[1])
	}
}

func TestMergeSimilar(t *testing.T) {
	lf := []discArtist{{Name: "Mayhem", Match: 0.7, Sources: []string{"lastfm"}}, {Name: "Darkthrone", Match: 1}, {Name: "Ildjarn", Match: 0.8}}
	dz := []discArtist{{Name: "Mayhem", DeezerID: "1", Match: 0.9, Sources: []string{srcDeezer}}, {Name: "Burzum", DeezerID: "2", Match: 0.5}}
	got := mergeSimilar("darkthrone", lf, dz, 10)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	// Mayhem sale en las dos: sube por encima de Ildjarn y se queda con el id de Deezer.
	if got[0].Name != "Mayhem" || got[0].DeezerID != "1" || got[0].Match != 1 || len(got[0].Sources) != 2 {
		t.Errorf("primero = %+v", got[0])
	}
	if got[1].Name != "Ildjarn" || got[2].Name != "Burzum" {
		t.Errorf("orden = %+v", got)
	}
}

func TestOwned(t *testing.T) {
	owned := []ownedAlbum{
		{dir: `C:\m\Darkthrone\A Blaze in the Northern Sky`, artist: "Darkthrone", album: "A Blaze in the Northern Sky"},
		{dir: `C:\m\Frost - Under the Hungarian Blackmoon`, artist: "Frost", album: "Under the Hungarian Blackmoon"},
	}
	if n := len(ownedBy(owned, "DARKTHRONE")); n != 1 {
		t.Errorf("ownedBy = %d", n)
	}
	if d := ownedDir(owned, "Darkthrone", "A Blaze in the Northern Sky (Remastered)"); d != owned[0].dir {
		t.Errorf("ownedDir = %q", d)
	}
	if d := ownedDir(owned, "Darkthrone", "Under a Funeral Moon"); d != "" {
		t.Errorf("ownedDir de uno que no está = %q", d)
	}
}

func TestLastfmSimilar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "k" || r.URL.Query().Get("method") != "artist.getsimilar" {
			t.Errorf("consulta: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("mbid") != "" {
			w.Write([]byte(`{"error":6,"message":"The artist you supplied could not be found"}`))
			return
		}
		w.Write([]byte(`{"similarartists":{"artist":[{"name":"Mayhem","mbid":"m1","match":"0.83"}]}}`))
	}))
	defer srv.Close()
	old := lastfmBase
	lastfmBase = srv.URL + "/"
	defer func() { lastfmBase = old }()

	// Con un MBID que Last.fm no conoce, vuelve a probar por el nombre.
	got, err := lastfmSimilar(context.Background(), "k", "Darkthrone", "x")
	if err != nil || len(got) != 1 || got[0].Name != "Mayhem" || got[0].Match != 0.83 || got[0].MBID != "m1" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
