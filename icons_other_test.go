//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// El icono para los gestores de archivos de Linux: sale del folder.ico, no
// reescribe nada si ya está al día y al quitarlo deja lo ajeno.
func TestFileManagerIcon(t *testing.T) {
	dir := t.TempDir()
	img := renderIcon(kindCD, placeholder())
	ico, err := encodeICO(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, iconFileName), ico, 0o644); err != nil {
		t.Fatal(err)
	}
	// Lo que ya hubiera de otros (Dolphin, .hidden a mano) se conserva.
	os.WriteFile(filepath.Join(dir, dirEntryName), []byte("[Desktop Entry]\nIcon=viejo.png\n[Otros]\nX=1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, hiddenListName), []byte("notas.txt\n"), 0o644)

	fileManagerIcon(dir)

	got, err := os.ReadFile(filepath.Join(dir, pngIconName))
	if err != nil || !bytes.Equal(got, icoPNG(ico)) || !bytes.HasPrefix(got, []byte("\x89PNG")) {
		t.Fatalf("%s mal escrito (%v)", pngIconName, err)
	}
	de, _ := os.ReadFile(filepath.Join(dir, dirEntryName))
	if want := "[Desktop Entry]\n" + dirEntryIcon + "\n[Otros]\nX=1\n"; string(de) != want {
		t.Errorf(".directory:\n%s\nquería:\n%s", de, want)
	}
	hid, _ := os.ReadFile(filepath.Join(dir, hiddenListName))
	if string(hid) != "notas.txt\ndesktop.ini\nfolder.ico\n" {
		t.Errorf(".hidden: %q", hid)
	}
	if _, err := exec.LookPath("gio"); err == nil {
		out, _ := exec.Command("gio", "info", "-a", "metadata::custom-icon", dir).Output()
		if !strings.Contains(string(out), pngIconName) {
			t.Logf("gio no guardó el icono (¿sin gvfs?): %s", out)
		}
	}

	// Otra vez, sin cambios: no se escribe nada (la fecha de la carpeta
	// manda en la caché de tarjetas).
	before, _ := os.Stat(dir)
	fileManagerIcon(dir)
	if after, _ := os.Stat(dir); !after.ModTime().Equal(before.ModTime()) {
		t.Error("se reescribió la carpeta sin haber cambios")
	}

	// Sin folder.ico (icono quitado): fuera lo nuestro, lo ajeno se queda.
	os.Remove(filepath.Join(dir, iconFileName))
	fileManagerIcon(dir)
	if fileExists(filepath.Join(dir, pngIconName)) {
		t.Error("queda el .folder.png")
	}
	if de, _ := os.ReadFile(filepath.Join(dir, dirEntryName)); string(de) != "[Desktop Entry]\n[Otros]\nX=1\n" {
		t.Errorf(".directory tras quitar: %q", de)
	}
	if hid, _ := os.ReadFile(filepath.Join(dir, hiddenListName)); string(hid) != "notas.txt\n" {
		t.Errorf(".hidden tras quitar: %q", hid)
	}

	// Una carpeta solo con lo nuestro queda limpia del todo.
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, iconFileName), ico, 0o644)
	fileManagerIcon(dir2)
	os.Remove(filepath.Join(dir2, iconFileName))
	fileManagerIcon(dir2)
	if entries, _ := os.ReadDir(dir2); len(entries) != 0 {
		t.Errorf("quedan ficheros: %v", entries)
	}
}
