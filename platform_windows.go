//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Lo que se hace distinto en Windows y en Linux, con el mismo nombre en los
// dos ficheros (platform_other.go es el de Linux): el resto del programa
// llama a openDir, pathKey, freeSpace, tieToApp o killOrphans sin preguntar
// el sistema (ocultar la consola de los subprocesos está en internal/tools,
// HideWindow).
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

// killJob es un job object con KILL_ON_JOB_CLOSE: su handle no se cierra
// nunca a mano, así que se cierra cuando muere la app, y Windows mata con
// ella todo lo que se haya metido dentro.
var killJob struct {
	once sync.Once
	h    windows.Handle
	err  error
}

// tieToApp ata un proceso ya arrancado a la vida de la app: si Trovador se
// cierra de golpe (o lo matan), el proceso muere también y no se queda
// huérfano ocupando su puerto (lo que le pasaba a slskd).
func tieToApp(cmd *exec.Cmd) error {
	killJob.once.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			killJob.err = err
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
		}
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			killJob.err = err
			return
		}
		killJob.h = h
	})
	if killJob.err != nil {
		return killJob.err
	}
	if cmd.Process == nil {
		return fmt.Errorf("el proceso no ha arrancado")
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(killJob.h, ph)
}

// killOrphans cierra los procesos que estén ejecutando exactamente ese
// programa (misma ruta): restos de una sesión anterior que acabó sin
// poder pararlos. Devuelve cuántos ha cerrado.
func killOrphans(exe string) int {
	want, err := filepath.Abs(exe)
	if err != nil {
		return 0
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	n := 0
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), filepath.Base(want)) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, e.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_PATH)
		size := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil &&
			strings.EqualFold(windows.UTF16ToString(buf[:size]), want) &&
			windows.TerminateProcess(h, 1) == nil {
			windows.WaitForSingleObject(h, 3000) // que suelte el puerto antes de seguir
			n++
		}
		windows.CloseHandle(h)
	}
	return n
}
