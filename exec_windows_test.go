//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// killOrphans cierra el proceso de esa ruta exacta y no otro con el mismo
// nombre de fichero.
func TestKillOrphans(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"))
	if err != nil {
		t.Skip("sin PING.EXE:", err)
	}
	dir, other := t.TempDir(), t.TempDir()
	exe := filepath.Join(dir, "huerfano.exe")
	twin := filepath.Join(other, "huerfano.exe") // mismo nombre, otra carpeta: no se toca
	for _, p := range []string{exe, twin} {
		if err := os.WriteFile(p, src, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	start := func(p string) *exec.Cmd {
		cmd := exec.Command(p, "-n", "30", "127.0.0.1")
		hideWindow(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	orphan, keep := start(exe), start(twin)
	defer keep.Process.Kill()
	defer orphan.Process.Kill()
	time.Sleep(300 * time.Millisecond)

	if n := killOrphans(exe); n != 1 {
		t.Fatalf("killOrphans = %d, quería 1", n)
	}
	done := make(chan error, 1)
	go func() { done <- orphan.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("el huérfano sigue vivo")
	}
	if keep.ProcessState != nil {
		t.Fatal("ha cerrado también el de la otra carpeta")
	}
}

// tieToApp: si la app muere de golpe, el hijo atado muere con ella. El
// binario de la prueba hace de app: se relanza a sí mismo, arranca un hijo
// atado, dice su PID y sale sin parar nada.
func TestTieToAppKillsChildWithApp(t *testing.T) {
	if os.Getenv("TROVADOR_TIE_PARENT") == "1" {
		cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"), "-n", "60", "127.0.0.1")
		hideWindow(cmd)
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		if err := tieToApp(cmd); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString(strconv.Itoa(cmd.Process.Pid))
		os.Exit(0) // de golpe: sin matar al hijo
	}
	parent := exec.Command(os.Args[0], "-test.run=^TestTieToAppKillsChildWithApp$")
	parent.Env = append(os.Environ(), "TROVADOR_TIE_PARENT=1")
	out, err := parent.Output()
	if err != nil {
		t.Fatalf("la «app»: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("PID: %q", out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			return // ya no existe
		}
		ev, _ := windows.WaitForSingleObject(h, 100)
		windows.CloseHandle(h)
		if ev == windows.WAIT_OBJECT_0 {
			return // ha terminado
		}
	}
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
	t.Fatal("el hijo atado sigue vivo tras morir la app")
}
