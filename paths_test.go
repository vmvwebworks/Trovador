package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Las mayúsculas cuentan en Linux y no en Windows.
func TestSamePathAndInsideOf(t *testing.T) {
	a, b := filepath.Join("m", "Foo"), filepath.Join("m", "foo")
	if samePath(a, b) != (runtime.GOOS == "windows") {
		t.Errorf("samePath(%q, %q) = %v en %s", a, b, samePath(a, b), runtime.GOOS)
	}
	if !samePath(a, a+string(filepath.Separator)) {
		t.Error("una barra final no cambia la ruta")
	}
	if !insideOf(filepath.Join(a, "disco"), a) || !insideOf(a, a) {
		t.Error("insideOf no ve lo que está dentro")
	}
	if insideOf(filepath.Join("m", "Foobar"), a) {
		t.Error("Foobar no está dentro de Foo")
	}
	if insideOf(filepath.Join(b, "disco"), a) != (runtime.GOOS == "windows") {
		t.Error("insideOf con otras mayúsculas")
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"Magdalena: Medieval Songs": "Magdalena - Medieval Songs",
		"AC/DC":                     "AC-DC",
		"...":                       "",
		"..":                        "",
		"Nul":                       "_Nul",
		"CON.live":                  "_CON.live",
		"Console":                   "Console",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, quiero %q", in, got, want)
		}
	}
}

func TestCheckFolderName(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, filepath.Join("..", "x")} {
		if checkFolderName(bad) == nil {
			t.Errorf("checkFolderName(%q) debería fallar", bad)
		}
	}
	if err := checkFolderName("Oliphant - Songs (1992)"); err != nil {
		t.Error(err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for _, data := range []string{"uno", "dos"} {
		if err := writeFileAtomic(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != data {
			t.Errorf("contenido %q, quiero %q", got, data)
		}
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("permisos %v", info.Mode().Perm())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("quedan temporales: %v", entries)
	}
}
