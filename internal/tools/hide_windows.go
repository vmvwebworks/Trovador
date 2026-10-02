//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

// HideWindow evita que cada yt-dlp/ffmpeg abra una ventana de consola
// negra al lanzarse desde una app gráfica. Este fichero solo se compila en
// Windows gracias a la etiqueta de build de la primera línea; en Linux,
// hide_other.go tiene la misma función sin hacer nada.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
