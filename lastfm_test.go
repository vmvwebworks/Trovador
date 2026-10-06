package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitArtists(t *testing.T) {
	cases := map[string][]string{
		"Krayl / Erroiak":                {"Krayl", "Erroiak"},
		"Saltatio Mortis, Versengold":    {"Saltatio Mortis", "Versengold"},
		"Darkthrone":                     {"Darkthrone"},
		"Mr. Hurley & Die Pulveraffen":   {"Mr. Hurley", "Die Pulveraffen"},
		"Burzum feat. Varg":              {"Burzum", "Varg"},
		"Lustre (Swe)":                   {"Lustre (Swe)"},
		"Brigitte Lesne & Pierre Hamon ": {"Brigitte Lesne", "Pierre Hamon"},
	}
	for in, want := range cases {
		if got := splitArtists(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitArtists(%q) = %q, quería %q", in, got, want)
		}
	}
}

func TestTagVectorAndCosine(t *testing.T) {
	bm := tagVector([]lfTag{{"black metal", 100}, {"norwegian", 40}, {"seen live", 90}}, 1, nil)
	if bm["seen live"] != 0 || bm["norwegian"] != 0.3*0.4 {
		t.Errorf("pesos: %v", bm)
	}
	pop := tagVector([]lfTag{{"pop", 100}, {"norwegian", 60}}, 1, nil)
	same := tagVector([]lfTag{{"black metal", 80}, {"raw black metal", 30}}, 1, nil)
	// Compartir solo el país apenas cuenta; compartir el estilo, mucho.
	if c := cosine(bm, pop); c > 0.05 {
		t.Errorf("black metal ~ pop noruego = %.3f, demasiado", c)
	}
	if c := cosine(bm, same); c < 0.8 {
		t.Errorf("black metal ~ black metal = %.3f, poco", c)
	}
	if cosine(bm, map[string]float64{}) != 0 {
		t.Error("sin perfil, 0")
	}
}

// Un Last.fm de mentira con las etiquetas de cada artista.
func fakeLastfmTags(t *testing.T, tags map[string]string) *int {
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		body, ok := tags[r.URL.Query().Get("artist")]
		if !ok {
			w.Write([]byte(`{"error":6,"message":"The artist you supplied could not be found"}`))
			return
		}
		w.Write([]byte(`{"toptags":{"tag":[` + body + `]}}`))
	}))
	t.Cleanup(srv.Close)
	old, oldPath, oldGap := lastfmBase, lfCachePath, lfThrottle.gap
	lastfmBase = srv.URL + "/"
	cache := filepath.Join(t.TempDir(), "lastfm-cache.json")
	lfCachePath = func() string { return cache }
	lfThrottle.gap = 0
	lfCache.loaded, lfCache.m = false, nil
	t.Cleanup(func() {
		lastfmBase, lfCachePath, lfThrottle.gap = old, oldPath, oldGap
		lfCache.loaded, lfCache.m = false, nil
	})
	return calls
}

func TestStyleFilter(t *testing.T) {
	calls := fakeLastfmTags(t, map[string]string{
		"Moss Troll": `{"name":"Dungeon Synth","count":100},{"name":"seen live","count":50}`,
		"Utred":      `{"name":"dungeon synth","count":100},{"name":"medieval","count":60}`,
		"Pop Star":   `{"name":"pop","count":100},{"name":"female vocalists","count":80}`,
	})
	profile := tagVector([]lfTag{{"dungeon synth", 100}, {"medieval", 56}}, 1, nil)
	list := []discArtist{{Name: "Pop Star", Match: 1}, {Name: "Moss Troll", Match: 0.8}, {Name: "Utred", Match: 0.75}, {Name: "Nadie", Match: 0.9}}
	got, dropped := styleFilter(context.Background(), "k", func(int) map[string]float64 { return profile }, list)
	if dropped != 1 {
		t.Errorf("descartados = %d, quería 1 (Pop Star)", dropped)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	// Los dos de dungeon synth pasan de 0,4 de estilo («el mismo»): manda el
	// parecido de Last.fm. A «Nadie» Last.fm no lo conoce: se queda, más
	// abajo (0,9 × 0,7).
	if !reflect.DeepEqual(names, []string{"Moss Troll", "Utred", "Nadie"}) {
		t.Errorf("orden = %v", names)
	}
	if !reflect.DeepEqual(got[0].Tags, []string{"dungeon synth"}) {
		t.Errorf("etiquetas de Moss Troll = %v (en minúsculas y sin «seen live»)", got[1].Tags)
	}

	// La segunda vez sale de la caché, también la de «no lo conozco».
	before := *calls
	styleFilter(context.Background(), "k", func(int) map[string]float64 { return profile }, list)
	if *calls != before {
		t.Errorf("sin caché: %d peticiones más", *calls-before)
	}
	lfSave()
	lfCache.loaded, lfCache.m = false, nil
	if _, ok := lfGet("tags:" + normalizeName("Utred")); !ok {
		t.Error("la caché no se ha guardado en disco")
	}
}
