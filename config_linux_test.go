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

// Guardar desde Descargas no pisa la colección ni lo de Soulseek, que se
// guardan desde otras pestañas.
func TestSaveSettingsKeepsOtherTabs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a := &App{settings: defaultSettings()}
	a.settings.Collection, a.settings.SlskUser = "/música/colección", "yo"

	old := defaultSettings() // la copia vieja de la ventana
	old.Format, old.Workers = "flac", 20
	got, err := a.SaveSettings(old)
	if err != nil {
		t.Fatal(err)
	}
	if got.Collection != "/música/colección" || got.SlskUser != "yo" {
		t.Errorf("se han perdido ajustes de otras pestañas: %+v", got)
	}
	if got.Format != "flac" || got.Workers != 8 {
		t.Errorf("no se han guardado los de Descargas: %+v", got)
	}
	if disk := loadSettings(); disk != got {
		t.Errorf("en disco:\n%+v\nquiero\n%+v", disk, got)
	}
}

// Un config.json roto se aparta en vez de perderse al guardar.
func TestLoadSettingsBroken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, _ := configPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"slskPass": "secreto",`), 0o600)
	if s := loadSettings(); s != defaultSettings() {
		t.Errorf("no son los de por defecto: %+v", s)
	}
	if data, err := os.ReadFile(path + ".roto"); err != nil || len(data) == 0 {
		t.Errorf("no se ha guardado la copia: %v", err)
	}
}
