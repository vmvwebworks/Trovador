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

// hideWindow evita que cada yt-dlp/ffmpeg abra una ventana de consola
// negra al lanzarse desde una app gráfica. Este fichero solo se compila en
// Windows gracias a la etiqueta de build de la primera línea.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
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
