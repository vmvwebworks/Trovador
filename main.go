// Trovador (antes music_picker): app de escritorio para descargar música de YouTube,
// YouTube Music y Bandcamp como MP3.
//
// Este programa NO extrae nada por sí mismo: delega en yt-dlp (extracción)
// y ffmpeg (conversión). Lo que aporta Go es la orquestación: descargas en
// paralelo, troceado de discos por capítulos, etiquetas, y la ventana.
//
// La ventana la pone Wails: un WebView nativo (WebView2 en Windows) que
// muestra frontend/index.html y puede llamar a los métodos de App (app.go).
package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// version la pone la release al compilar (wails build -ldflags "-X main.version=1.2.0");
// compilando a mano queda "dev".
var version = "dev"

// //go:embed mete la carpeta frontend/ dentro del binario al compilar. El
// .exe resultante es autosuficiente: no hay que copiar la interfaz al lado.
//
//go:embed all:frontend
var frontend embed.FS

func main() {
	assets, err := fs.Sub(frontend, "frontend")
	if err != nil {
		log.Fatal(err)
	}

	app := NewApp()
	err = wails.Run(&options.App{
		Title:            "Trovador",
		Width:            980,
		Height:           760,
		MinWidth:         600,
		MinHeight:        480,
		BackgroundColour: &options.RGBA{R: 15, G: 17, B: 21, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets, Handler: app.mediaHandler()}, // /media?p= sirve el audio al reproductor
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		// Bind: los structs cuyos métodos exportados verá el JavaScript.
		Bind: []any{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
