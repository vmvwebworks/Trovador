//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// hideWindow evita que cada yt-dlp/ffmpeg abra una ventana de consola
// negra al lanzarse desde una app gráfica. Este fichero solo se compila en
// Windows gracias a la etiqueta de build de la primera línea.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// openDir abre una carpeta en el Explorador.
func openDir(dir string) error { return exec.Command("explorer", dir).Start() }

// pathKey es la forma de una ruta para compararla con otra: en Windows las
// mayúsculas no cuentan (C:\Música y c:\música son la misma carpeta).
func pathKey(p string) string { return strings.ToLower(filepath.Clean(p)) }
