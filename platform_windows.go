//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Lo que se hace distinto en Windows y en Linux, con el mismo nombre en los
// dos ficheros (platform_other.go es el de Linux): el resto del programa
// llama a openDir, pathKey o freeSpace sin preguntar el sistema (ocultar la
// consola de los subprocesos está en internal/tools, HideWindow).
// Los iconos de carpeta tienen los suyos en icons_windows.go/icons_other.go.

// openDir abre una carpeta en el Explorador.
func openDir(dir string) error { return exec.Command("explorer", dir).Start() }

// pathKey es la forma de una ruta para compararla con otra: en Windows las
// mayúsculas no cuentan (C:\Música y c:\música son la misma carpeta).
func pathKey(p string) string { return strings.ToLower(filepath.Clean(p)) }

// freeSpace: bytes libres en la unidad de la carpeta (-1 si no se sabe).
func freeSpace(dir string) int64 {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	proc := k32.NewProc("GetDiskFreeSpaceExW")
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return -1
	}
	var free, total, totalFree uint64
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return -1
	}
	return int64(free)
}
