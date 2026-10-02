//go:build !windows

package main

import (
	"bytes"
	"encoding/binary"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	attrReadonly = 0x1
	attrHidden   = 0x2
	attrSystem   = 0x4
)

// Fuera de Windows no hay atributos de este tipo: los ficheros se escriben
// igual (desktop.ini y folder.ico guardan el tipo y la firma del icono, y
// así la carpeta se sigue viendo bien si se abre desde Windows).
func setAttrs(path string, set, clear uint32) error { return nil }
func shellRefreshIcons()                            {}

// En Linux el icono sale del mismo folder.ico: su PNG de 256 se copia a
// .folder.png (oculto) y se le dice al gestor de archivos. Nautilus, Nemo y
// Caja lo leen de los metadatos de gvfs (gio set ... metadata::custom-icon);
// Dolphin, de un .directory dentro de la carpeta. desktop.ini y folder.ico
// se apuntan en .hidden, que todos ellos respetan, para que no estorben.
const (
	pngIconName    = ".folder.png"
	dirEntryName   = ".directory"
	hiddenListName = ".hidden"
	dirEntryIcon   = "Icon=./" + pngIconName
)

// shellNotify: tras crear o quitar el icono, se pone al día lo de Linux.
func shellNotify(dir string) { fileManagerIcon(dir) }

// fileManagerIcon deja la carpeta como dice su folder.ico (o sin icono si no
// lo tiene). Solo escribe lo que cambia: escribir cambia la fecha de la
// carpeta y tiraría la caché de su tarjeta.
func fileManagerIcon(dir string) {
	ico, err := os.ReadFile(filepath.Join(dir, iconFileName))
	if err != nil {
		clearFileManagerIcon(dir)
		return
	}
	data := icoPNG(ico)
	if data == nil {
		return
	}
	pngPath := filepath.Join(dir, pngIconName)
	changed := false
	if old, err := os.ReadFile(pngPath); err != nil || !bytes.Equal(old, data) {
		if err := os.WriteFile(pngPath, data, 0o644); err != nil {
			return
		}
		changed = true
	}
	editDirEntry(dir, true)
	editHiddenList(dir, true)
	if changed {
		// Mismo nombre, otro dibujo: quitarlo y volverlo a poner es lo que
		// hace que Nautilus/Nemo lo repinten en vez de usar el que tenían.
		gio("set", "-t", "unset", dir, "metadata::custom-icon")
	}
	gio("set", dir, "metadata::custom-icon", (&url.URL{Scheme: "file", Path: pngPath}).String())
}

// clearFileManagerIcon deshace fileManagerIcon (lo que no es nuestro se queda).
func clearFileManagerIcon(dir string) {
	if os.Remove(filepath.Join(dir, pngIconName)) != nil {
		return // no había nada nuestro
	}
	editDirEntry(dir, false)
	editHiddenList(dir, false)
	gio("set", "-t", "unset", dir, "metadata::custom-icon")
}

// gio lanza la herramienta de GLib; sin ella (otro escritorio) no pasa nada.
func gio(args ...string) {
	_ = exec.Command("gio", args...).Run()
}

// icoPNG saca la imagen de 256 (guardada como PNG) de un .ico de encodeICO.
func icoPNG(ico []byte) []byte {
	if len(ico) < 6 {
		return nil
	}
	le := binary.LittleEndian
	n := int(le.Uint16(ico[4:]))
	for i := 0; i < n; i++ {
		e := 6 + 16*i
		if e+16 > len(ico) {
			return nil
		}
		if ico[e] != 0 { // ancho 0 = 256
			continue
		}
		size, off := int(le.Uint32(ico[e+8:])), int(le.Uint32(ico[e+12:]))
		if off+size > len(ico) || !bytes.HasPrefix(ico[off:], []byte("\x89PNG")) {
			return nil
		}
		return ico[off : off+size]
	}
	return nil
}

// editDirEntry pone (o quita) nuestra línea Icon= en el .directory de
// Dolphin, conservando lo demás; si solo quedaba lo nuestro, lo borra.
func editDirEntry(dir string, on bool) {
	path := filepath.Join(dir, dirEntryName)
	raw, _ := os.ReadFile(path)
	var out []string
	section, placed := "", false
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = t
		}
		if section == "[Desktop Entry]" && strings.HasPrefix(t, "Icon=") {
			continue // la nuestra (u otra que la nuestra sustituye)
		}
		if t != "" || len(out) > 0 {
			out = append(out, line)
		}
		if on && t == "[Desktop Entry]" && !placed {
			out = append(out, dirEntryIcon)
			placed = true
		}
	}
	if on && !placed {
		out = append([]string{"[Desktop Entry]", dirEntryIcon}, out...)
	}
	text := strings.Join(out, "\n") + "\n"
	if !on && strings.TrimSpace(text) == "[Desktop Entry]" || strings.TrimSpace(text) == "" {
		os.Remove(path)
		return
	}
	if text != string(raw) {
		_ = os.WriteFile(path, []byte(text), 0o644)
	}
}

// editHiddenList apunta (o quita) desktop.ini y folder.ico en .hidden.
func editHiddenList(dir string, on bool) {
	path := filepath.Join(dir, hiddenListName)
	raw, _ := os.ReadFile(path)
	ours := map[string]bool{iniFileName: true, iconFileName: true}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(line); t != "" && !ours[t] {
			out = append(out, t)
		}
	}
	if on {
		out = append(out, iniFileName, iconFileName)
	}
	if len(out) == 0 {
		os.Remove(path)
		return
	}
	if text := strings.Join(out, "\n") + "\n"; text != string(raw) {
		_ = os.WriteFile(path, []byte(text), 0o644)
	}
}

// freeSpace: bytes libres en el disco de la carpeta (-1 si no se sabe).
func freeSpace(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
