//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

// La versión de Linux (y macOS) de platform_windows.go.

// openDir abre una carpeta en el gestor de archivos del escritorio.
func openDir(dir string) error { return exec.Command("xdg-open", dir).Start() }

// pathKey es la forma de una ruta para compararla con otra: en Linux las
// mayúsculas cuentan (/m/Foo y /m/foo son dos carpetas distintas).
func pathKey(p string) string { return filepath.Clean(p) }

// freeSpace: bytes libres en el disco de la carpeta (-1 si no se sabe).
func freeSpace(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
