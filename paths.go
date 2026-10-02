package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Rutas y ficheros: comprobar si existen, compararlas (con las reglas de
// mayúsculas de cada sistema, ver pathKey en exec_*.go), y copiar, mover o
// escribir sin dejar nada a medias.

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// writeFileAtomic escribe en un temporal al lado y luego lo renombra encima:
// si la app se cierra (o se va la luz) a medias, queda el fichero viejo
// entero en vez de uno cortado.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no hace nada si el rename ha ido bien
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// samePath: las dos rutas son la misma, con las reglas de mayúsculas del
// sistema (ver pathKey).
func samePath(a, b string) bool { return pathKey(a) == pathKey(b) }

// insideOf: p está dentro de dir (o es dir).
func insideOf(p, dir string) bool {
	p, dir = pathKey(p), pathKey(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// moveDir mueve una carpeta; si está en otra unidad (rename no vale), la
// copia fichero a fichero y borra el origen.
func moveDir(ctx context.Context, from, to string) error {
	if insideOf(to, from) {
		return fmt.Errorf("el destino %s está dentro del origen", to)
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	// Otra unidad, o un fichero abierto: copia y borrado.
	if err := copyTree(ctx, from, to); err != nil {
		os.RemoveAll(to)
		return fmt.Errorf("no pude mover a %s: %v", to, err)
	}
	return os.RemoveAll(from)
}

func copyTree(ctx context.Context, from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(from, path)
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
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

// existingDir devuelve la carpeta si existe o, si no, la primera de sus
// carpetas madre que exista. El diálogo nativo de Windows falla (sin abrirse)
// si se le pide arrancar en una ruta inexistente, y la carpeta de descargas
// no se crea hasta la primera descarga.
func existingDir(dir string) string {
	for dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}
