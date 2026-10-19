package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
)

// Servidor de audio por loopback (solo Linux). WebKitGTK no hace streaming de
// un esquema propio (no hay Range) y reproducir un Blob entero en memoria
// provoca saltos en máquinas justas; por HTTP normal, con Range, va como
// cualquier página. La ruta lleva un token aleatorio para que ninguna otra
// página del equipo pueda leer ficheros por ahí.
var (
	mediaSrvOnce sync.Once
	mediaSrvBase string
)

// MediaBase devuelve la base http://127.0.0.1:<puerto>/<token> desde la que el
// reproductor puede pedir /media?p=..., o "" si hay que usar el Blob de siempre.
func (a *App) MediaBase() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	mediaSrvOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			ln.Close()
			return
		}
		token := hex.EncodeToString(b)
		inner := a.mediaHandler()
		mux := http.NewServeMux()
		mux.HandleFunc("/"+token+"/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*") // el analizador de Web Audio lo exige
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, "/"+token+"/")
			inner.ServeHTTP(w, r2)
		})
		go http.Serve(ln, mux)
		mediaSrvBase = "http://" + ln.Addr().String() + "/" + token
	})
	return mediaSrvBase
}
