package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLastfmSig(t *testing.T) {
	// md5("api_keyKmethodauth.getTokenS"), calculado aparte (format no entra).
	p := url.Values{"method": {"auth.getToken"}, "api_key": {"K"}, "format": {"json"}}
	if got := lastfmSig(p, "S"); got != "6853c6d6f1ccd87e06f779211e32567c" {
		t.Errorf("firma = %s", got)
	}
}

// Un Last.fm de mentira que comprueba la firma de cada petición y anota los
// scrobbles que recibe. fail hace que conteste con un error.
func fakeLastfm(t *testing.T, fail *bool, got *[]url.Values) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p := r.Form
		if p.Get("api_sig") != lastfmSig(p, "secret") {
			t.Errorf("firma incorrecta en %s", p.Get("method"))
		}
		if *fail {
			w.Write([]byte(`{"error":11,"message":"Service Offline"}`))
			return
		}
		*got = append(*got, p)
		w.Write([]byte(`{"scrobbles":{"@attr":{"accepted":1,"ignored":0}}}`))
	}))
	t.Cleanup(srv.Close)
	old := lastfmBase
	lastfmBase = srv.URL + "/"
	t.Cleanup(func() { lastfmBase = old })
}

func TestScrobbleQueue(t *testing.T) {
	file := filepath.Join(t.TempDir(), "scrobbles.json")
	oldPath := queuePath
	queuePath = func() string { return file }
	t.Cleanup(func() { queuePath = oldPath })
	scrobbler.queue, scrobbler.loaded, scrobbler.lastErr = nil, false, ""
	oldLater := flushLater
	flushLater = func(*App) {} // aquí se envía a mano, sin goroutines
	t.Cleanup(func() { flushLater = oldLater })

	fail := true
	var got []url.Values
	fakeLastfm(t, &fail, &got)
	a := &App{ctx: context.Background(), emit: func(string, ...any) {}}
	a.settings = settings{LastfmKey: "key", LastfmSecret: "secret", LastfmSession: "sk", Scrobble: true}

	// Last.fm caído: la escucha se queda en disco.
	a.Scrobble(scrobbleTrack{Artist: "Darkthrone", Track: "Transilvanian Hunger", Album: "Transilvanian Hunger", Duration: 370, Timestamp: 100})
	a.flushScrobbles()
	if st := a.LastfmState(); st.Pending != 1 || st.Error == "" {
		t.Fatalf("estado tras fallo: %+v", st)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("la cola no está en disco: %v", err)
	}

	// Vuelve: sale la pendiente junto con la nueva, en una sola petición.
	fail = false
	a.Scrobble(scrobbleTrack{Artist: "Burzum", Track: "Dunkelheit", Timestamp: 200})
	a.flushScrobbles()
	if st := a.LastfmState(); st.Pending != 0 || st.Error != "" {
		t.Fatalf("estado tras enviar: %+v", st)
	}
	var batch url.Values
	for _, p := range got {
		if p.Get("method") == "track.scrobble" && p.Get("artist[1]") != "" {
			batch = p
		}
	}
	if batch == nil || batch.Get("artist[0]") != "Darkthrone" || batch.Get("track[1]") != "Dunkelheit" ||
		batch.Get("timestamp[0]") != "100" || batch.Get("duration[0]") != "370" || batch.Get("sk") != "sk" {
		t.Fatalf("peticiones: %v", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("con la cola vacía, el fichero sobra")
	}

	// Sin scrobbling activado no se apunta nada.
	a.settings.Scrobble = false
	a.Scrobble(scrobbleTrack{Artist: "X", Track: "Y"})
	if st := a.LastfmState(); st.Pending != 0 {
		t.Errorf("desactivado y apunta: %+v", st)
	}
}
