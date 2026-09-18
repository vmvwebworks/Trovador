package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Llevarse discos: copiar los discos marcados en la Biblioteca a otra
// carpeta (el USB del coche), cada uno en su carpeta «Artista - Álbum» (o
// Artista\Álbum), con nombres aptos para FAT32, solo el audio y las
// carátulas (sin desktop.ini, folder.ico ni _original) y, si se pide,
// convirtiendo a MP3 lo que no lo sea (los coches no suelen tragar FLAC ni
// Opus). Antes de copiar, el plan: qué va, cuánto ocupa y si cabe.

type carryOptions struct {
	Dirs    []string `json:"dirs"`
	Dest    string   `json:"dest"`
	Layout  string   `json:"layout"`  // flat: «Artista - Álbum»; artist: Artista\Álbum
	Convert bool     `json:"convert"` // a MP3 lo que no sea MP3
	Format  string   `json:"format"`  // mp3-320 | mp3-v0
	Replace bool     `json:"replace"` // los que ya estén en destino, otra vez
}

type carryItem struct {
	Dir     string `json:"dir"`
	Name    string `json:"name"`   // Artista - Álbum
	Target  string `json:"target"` // carpeta de destino
	Files   int    `json:"files"`  // pistas
	Convert int    `json:"convert"`
	Bytes   int64  `json:"bytes"` // a escribir (las convertidas, a ojo)
	Exists  bool   `json:"exists"`
	Skip    string `json:"skip,omitempty"`
}

type carryPlan struct {
	Items []carryItem `json:"items"`
	Files int         `json:"files"`
	Bytes int64       `json:"bytes"`
	Free  int64       `json:"free"`
	Fits  bool        `json:"fits"`
}

type carryProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Album   string `json:"album"`
	File    string `json:"file"`
	Running bool   `json:"running"`
	Errors  int    `json:"errors"`
}

type carryRun struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

var carry carryRun

var badFATChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

// fatName deja un nombre apto para FAT32 (el USB del coche): sin los
// caracteres prohibidos, sin punto ni espacio al final, no muy largo.
func fatName(s string) string {
	s = badFATChars.ReplaceAllString(s, "_")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ". ")
	if len(s) > 100 {
		s = strings.TrimRight(s[:100], ". ")
	}
	if s == "" {
		s = "_"
	}
	return s
}

// carryNames: artista y título de un disco, de sus etiquetas o del nombre
// de la carpeta.
func (a *App) carryNames(dir string) (artist, album string) {
	if card, err := a.AlbumCard(dir); err == nil {
		artist, album = firstNonEmpty(card.TagArtist, card.Artist), firstNonEmpty(card.TagAlbum, card.Album)
	}
	if album == "" {
		album = filepath.Base(dir)
	}
	if artist == "" {
		artist = "Varios"
	}
	return artist, album
}

// carryFiles: lo que se lleva de un disco (audio e imágenes, a cualquier
// profundidad, sin las carpetas apartadas) con su ruta relativa.
func carryFiles(dir string) (files []string, err error) {
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "$")) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if audioExts[ext] || ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func (o carryOptions) format() (convFormat, error) {
	f, ok := convFormats[o.Format]
	if !ok || f.Ext != ".mp3" {
		return convFormat{}, fmt.Errorf("formato para el coche desconocido: %s", o.Format)
	}
	return f, nil
}

// CarryPlan: para cada disco, su carpeta de destino, cuántas pistas van y
// cuántas se convertirían, y si cabe todo en el destino.
func (a *App) CarryPlan(o carryOptions) (carryPlan, error) {
	var plan carryPlan
	if o.Dest == "" {
		return plan, fmt.Errorf("elige la carpeta de destino")
	}
	if o.Convert {
		if _, err := o.format(); err != nil {
			return plan, err
		}
	}
	for _, dir := range o.Dirs {
		artist, album := a.carryNames(dir)
		it := carryItem{Dir: dir, Name: artist + " - " + album}
		if o.Layout == "artist" {
			it.Target = filepath.Join(o.Dest, fatName(artist), fatName(album))
		} else {
			it.Target = filepath.Join(o.Dest, fatName(it.Name))
		}
		if insideOf(it.Target, dir) {
			it.Skip = "el destino es la propia carpeta"
			plan.Items = append(plan.Items, it)
			continue
		}
		files, err := carryFiles(dir)
		if err != nil {
			it.Skip = err.Error()
			plan.Items = append(plan.Items, it)
			continue
		}
		for _, rel := range files {
			ext := strings.ToLower(filepath.Ext(rel))
			info, err := os.Stat(filepath.Join(dir, rel))
			if err != nil {
				continue
			}
			if audioExts[ext] {
				it.Files++
				if o.Convert && ext != ".mp3" {
					it.Convert++
					// A ojo: un MP3 a 320 kb/s ocupa la tercera parte de un FLAC y
					// la vigésima de un WAV; lo demás, más o menos lo mismo.
					switch ext {
					case ".flac", ".ape", ".wv", ".aiff", ".aif":
						it.Bytes += info.Size() / 3
					case ".wav":
						it.Bytes += info.Size() / 20
					default:
						it.Bytes += info.Size()
					}
					continue
				}
			}
			it.Bytes += info.Size()
		}
		if it.Files == 0 {
			it.Skip = "sin audio"
		} else if n := countAudio(it.Target); n >= it.Files {
			it.Exists = true
			if !o.Replace {
				it.Skip = "ya está en el destino"
			}
		}
		if it.Skip == "" {
			plan.Files += it.Files
			plan.Bytes += it.Bytes
		}
		plan.Items = append(plan.Items, it)
	}
	plan.Free = freeSpace(o.Dest)
	plan.Fits = plan.Free < 0 || plan.Bytes < plan.Free
	return plan, nil
}

// CarryApply copia (o convierte) disco a disco, avisando del avance con el
// evento "carry". Los que se saltan en el plan, se saltan.
func (a *App) CarryApply(o carryOptions) (carryProgress, error) {
	carry.mu.Lock()
	if carry.cancel != nil {
		carry.mu.Unlock()
		return carryProgress{}, fmt.Errorf("ya hay una copia en marcha")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	carry.cancel = cancel
	carry.mu.Unlock()
	defer func() {
		cancel()
		carry.mu.Lock()
		carry.cancel = nil
		carry.mu.Unlock()
	}()

	plan, err := a.CarryPlan(o)
	if err != nil {
		return carryProgress{}, err
	}
	var f convFormat
	if o.Convert {
		f, _ = o.format()
	}
	p := carryProgress{Total: plan.Files, Running: true}
	a.emit("carry", p)
	for _, it := range plan.Items {
		if it.Skip != "" {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		p.Album = it.Name
		files, err := carryFiles(it.Dir)
		if err != nil {
			a.libLog("%s: %v", it.Name, err)
			p.Errors++
			continue
		}
		a.libLog("Copiando %s -> %s", it.Name, it.Target)
		for _, rel := range files {
			if ctx.Err() != nil {
				break
			}
			src := filepath.Join(it.Dir, rel)
			ext := strings.ToLower(filepath.Ext(rel))
			// La ruta relativa también en nombres FAT32, parte a parte.
			parts := strings.Split(filepath.ToSlash(rel), "/")
			for i, s := range parts {
				parts[i] = fatName(s)
			}
			dst := filepath.Join(append([]string{it.Target}, parts...)...)
			p.File = filepath.Base(rel)
			a.emit("carry", p)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				a.libLog("%s: %v", it.Name, err)
				p.Errors++
				break
			}
			if o.Convert && audioExts[ext] && ext != ".mp3" {
				dst = strings.TrimSuffix(dst, filepath.Ext(dst)) + ".mp3"
				err = transcode(ctx, src, dst, f)
			} else {
				err = copyFile(ctx, src, dst)
			}
			if err != nil && ctx.Err() == nil {
				a.libLog("%s: %s: %v", it.Name, rel, err)
				p.Errors++
			}
			if audioExts[ext] {
				p.Done++
			}
		}
	}
	p.Running = false
	p.File = ""
	a.emit("carry", p)
	if ctx.Err() != nil {
		a.libLog("Copia cancelada: %d de %d pistas", p.Done, p.Total)
		return p, fmt.Errorf("cancelado")
	}
	a.libLog("Copia terminada: %d pistas, %d error(es)", p.Done, p.Errors)
	return p, nil
}

// CancelCarry para la copia en marcha (la pista a medias se queda a medias).
func (a *App) CancelCarry() {
	carry.mu.Lock()
	defer carry.mu.Unlock()
	if carry.cancel != nil {
		carry.cancel()
	}
}

// ChooseCarryFolder: el diálogo de elegir la carpeta de destino.
func (a *App) ChooseCarryFolder(current string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Carpeta de destino (el USB del coche)",
		DefaultDirectory: existingDir(current),
	})
}

// copyFile copia un fichero por partes, atento a la cancelación; a medias
// no deja nada.
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			out.Close()
			os.Remove(tmp)
			return ctx.Err()
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

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
