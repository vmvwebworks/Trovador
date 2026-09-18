//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	attrReadonly = 0x1
	attrHidden   = 0x2
	attrSystem   = 0x4
)

// setAttrs pone y quita atributos de fichero/carpeta de Windows.
func setAttrs(path string, set, clear uint32) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	a, err := syscall.GetFileAttributes(p)
	if err != nil {
		return err
	}
	return syscall.SetFileAttributes(p, a&^clear|set)
}

var shChangeNotify = syscall.NewLazyDLL("shell32.dll").NewProc("SHChangeNotify")

// shellNotify avisa al Explorador de que la carpeta ha cambiado, para que
// vuelva a leer su desktop.ini y repinte el icono sin esperar a un F5. Va
// como UPDATEDIR (esto es una carpeta, no un fichero): UPDATEITEM deja a
// veces el icono viejo —o un cuadro negro— en la caché del Explorador.
func shellNotify(dir string) {
	const (
		shcneUpdateDir   = 0x00001000
		shcnfPathW       = 0x0005
		shcnfFlushNoWait = 0x2000
	)
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	shChangeNotify.Call(shcneUpdateDir, shcnfPathW|shcnfFlushNoWait, uintptr(unsafe.Pointer(p)), 0)
}

// shellRefreshIcons le dice al Explorador que tire sus iconos cacheados.
// Tras rehacer cientos de folder.ico de golpe, avisar carpeta a carpeta no
// basta: la caché de iconos se queda con lo viejo y en los tamaños grandes
// llega a pintar cuadros negros. Esto es justo lo que hacen las utilidades
// de "refrescar iconos", y se manda una sola vez al terminar el lote.
func shellRefreshIcons() {
	const (
		shcneAssocChanged = 0x08000000
		shcnfIDList       = 0x0000
		shcnfFlush        = 0x1000
	)
	shChangeNotify.Call(shcneAssocChanged, shcnfIDList|shcnfFlush, 0, 0)

	// Y además la caché en disco (iconcache_*.db), que guarda una copia por
	// tamaño. Avisar no la vacía: se queda con el icono que tenía para un
	// tamaño y para otro no tiene nada, y entonces el Explorador pinta el
	// pequeño en medio del hueco grande sin estirarlo. "ie4uinit -show" es
	// la forma de Windows de rehacerla, y no cierra el Explorador.
	cmd := exec.Command("ie4uinit.exe", "-show")
	hideWindow(cmd)
	_ = cmd.Run()
}
