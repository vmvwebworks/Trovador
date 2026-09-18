//go:build !windows

package main

const (
	attrReadonly = 0x1
	attrHidden   = 0x2
	attrSystem   = 0x4
)

// Fuera de Windows no hay atributos de este tipo ni Explorador al que avisar:
// los ficheros se escriben igual (por si la carpeta se ve luego desde
// Windows) y no pasa nada más.
func setAttrs(path string, set, clear uint32) error { return nil }
func shellNotify(dir string)                        {}
func shellRefreshIcons()                            {}
