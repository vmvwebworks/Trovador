package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPreviewHandler(t *testing.T) {
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream") // Bandcamp no siempre dice que es audio
		w.Write([]byte("ID3audio"))
	}))
	defer src.Close()
	h := (&App{}).mediaHandler()
	get := func(u string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/preview?u="+url.QueryEscape(u), nil))
		return rec
	}

	rec := get(src.URL + "/track.mp3")
	if rec.Code != 200 || rec.Body.String() != "ID3audio" || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("reenvío: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec := get(src.URL + "/missing"); rec.Code != http.StatusBadGateway {
		t.Errorf("origen 404 → %d, quería 502", rec.Code)
	}
	for _, bad := range []string{`file:///C:/Windows/win.ini`, `C:\Windows\win.ini`, ""} {
		if rec := get(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%q → %d, quería 400", bad, rec.Code)
		}
	}
}

func TestHasStreams(t *testing.T) {
	if hasStreams(mbReleaseDetail{Tracks: []mbTrack{{Title: "a"}}}) {
		t.Error("sin Stream no hay audio")
	}
	if !hasStreams(mbReleaseDetail{Tracks: []mbTrack{{Title: "a"}, {Title: "b", Stream: "https://x"}}}) {
		t.Error("con una pista basta")
	}
}
