//go:build !windows

package main

import "os/exec"

// En Linux/macOS no hay ventanas de consola que ocultar.
func hideWindow(cmd *exec.Cmd) {}

// Ni job objects: los procesos hijos no se atan a la app.
func tieToApp(cmd *exec.Cmd) error { return nil }

func killOrphans(exe string) int { return 0 }
