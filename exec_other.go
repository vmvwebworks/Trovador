//go:build !windows

package main

import "os/exec"

// En Linux/macOS no hay ventanas de consola que ocultar.
func hideWindow(cmd *exec.Cmd) {}

// openDir abre una carpeta en el gestor de archivos del escritorio.
func openDir(dir string) error { return exec.Command("xdg-open", dir).Start() }
