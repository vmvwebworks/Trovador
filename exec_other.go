//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
)

// En Linux/macOS no hay ventanas de consola que ocultar.
func hideWindow(cmd *exec.Cmd) {}

// openDir abre una carpeta en el gestor de archivos del escritorio.
func openDir(dir string) error { return exec.Command("xdg-open", dir).Start() }

// pathKey es la forma de una ruta para compararla con otra: en Linux las
// mayúsculas cuentan (/m/Foo y /m/foo son dos carpetas distintas).
func pathKey(p string) string { return filepath.Clean(p) }
