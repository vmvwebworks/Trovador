//go:build !windows

package main

import "os/exec"

// En Linux/macOS no hay ventanas de consola que ocultar.
func hideWindow(cmd *exec.Cmd) {}
