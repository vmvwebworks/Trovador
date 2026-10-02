//go:build !windows

package tools

import "os/exec"

// HideWindow no hace nada en Linux/macOS: no hay ventanas de consola que
// ocultar.
func HideWindow(cmd *exec.Cmd) {}
