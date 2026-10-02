package main

import (
	"os"
	"path/filepath"
	"testing"
)

// La carpeta de música sale de user-dirs.dirs (el nombre va según idioma).
func TestMusicDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	home, _ := os.UserHomeDir()

	if got := musicDir(); got != filepath.Join(home, "Music") {
		t.Errorf("sin user-dirs.dirs: %s", got)
	}
	os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte("# comentario\nXDG_DESKTOP_DIR=\"$HOME/Escritorio\"\nXDG_MUSIC_DIR=\"$HOME/Música\"\n"), 0o644)
	if got := musicDir(); got != filepath.Join(home, "Música") {
		t.Errorf("en español: %s", got)
	}
	os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte("XDG_MUSIC_DIR=\"$HOME/\"\n"), 0o644)
	if got := musicDir(); got != filepath.Join(home, "Music") {
		t.Errorf("desactivada: %s", got)
	}
}
