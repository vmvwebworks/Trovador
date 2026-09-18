package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/text/encoding/unicode"
)

// Icono de carpeta para el Explorador. Windows lo lee de un desktop.ini
// (oculto) dentro de la carpeta, que apunta a un .ico también dentro; la
// carpeta lleva el atributo de solo lectura, que es la marca que usa el
// propio Explorador para "carpeta personalizada" (no impide tocar lo que
// hay dentro). Todo va con rutas relativas: la carpeta se puede mover.

const (
	iconFileName = "folder.ico"
	iniFileName  = "desktop.ini"
	iniSection   = "music_picker" // nuestra sección en el desktop.ini (se conserva el nombre antiguo: los que ya hay la llevan)
)

// folderIconInfo es lo que la ficha muestra sobre el icono de una carpeta.
type folderIconInfo struct {
	Kind    string `json:"kind"`    // con qué tipo se dibuja
	Fixed   bool   `json:"fixed"`   // el usuario lo fijó a mano
	Source  string `json:"source"`  // de dónde sale el tipo: fijado, edición, etiquetas, desconocido
	Format  string `json:"format"`  // texto del formato (p. ej. 12" Vinyl)
	Exists  bool   `json:"exists"`  // la carpeta ya tiene icono nuestro
	Preview string `json:"preview"` // PNG (data URL) del icono que se dibujaría
}

// iniData es lo que leemos de un desktop.ini existente.
type iniData struct {
	kind, format string   // Kind= fijado por el usuario, Format= texto
	auto         string   // Auto= tipo puesto automáticamente
	sig          string   // Sig= con qué se generó (para saltar los que no cambian)
	shellExtra   []string // otras claves de [.ShellClassInfo] (de otro programa)
	rest         []string // otras secciones enteras, tal cual
}

var utf16 = unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)

// readIni lee el desktop.ini de la carpeta (UTF-16 con BOM, o ANSI/UTF-8 si
// lo escribió otro) y separa lo nuestro de lo ajeno para conservarlo.
func readIni(dir string) iniData {
	var d iniData
	raw, err := os.ReadFile(filepath.Join(dir, iniFileName))
	if err != nil {
		return d
	}
	text := string(raw)
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		if dec, err := utf16.NewDecoder().Bytes(raw); err == nil {
			text = string(dec)
		}
	}
	ours := map[string]bool{"iconresource": true, "iconfile": true, "iconindex": true, "infotip": true}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.ToLower(t[1 : len(t)-1])
			if section != ".shellclassinfo" && section != iniSection {
				d.rest = append(d.rest, line)
			}
			continue
		}
		key, val, _ := strings.Cut(t, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		switch section {
		case iniSection:
			switch key {
			case "kind":
				d.kind = strings.TrimSpace(val)
			case "format":
				d.format = strings.TrimSpace(val)
			case "auto":
				d.auto = strings.TrimSpace(val)
			case "sig":
				d.sig = strings.TrimSpace(val)
			}
		case ".shellclassinfo":
			if t != "" && !ours[key] {
				d.shellExtra = append(d.shellExtra, line)
			}
		default:
			if section != "" && t != "" {
				d.rest = append(d.rest, line)
			}
		}
	}
	return d
}

// writeIni escribe el desktop.ini con nuestro icono, conservando lo que
// hubiera de otros programas. kind vacío = automático (se guarda en Auto=).
func writeIni(dir string, prev iniData, kind string, fixed bool, format, tip string) error {
	var b strings.Builder
	b.WriteString("[.ShellClassInfo]\r\n")
	b.WriteString("IconResource=" + iconFileName + ",0\r\n")
	if tip != "" {
		b.WriteString("InfoTip=" + strings.NewReplacer("\r", " ", "\n", " ").Replace(tip) + "\r\n")
	}
	for _, l := range prev.shellExtra {
		b.WriteString(l + "\r\n")
	}
	b.WriteString("[" + iniSection + "]\r\n")
	if fixed {
		b.WriteString("Kind=" + kind + "\r\n")
	} else {
		b.WriteString("Auto=" + kind + "\r\n")
	}
	if format != "" {
		b.WriteString("Format=" + format + "\r\n")
	}
	b.WriteString("Sig=" + iconSig(dir, kind) + "\r\n")
	for _, l := range prev.rest {
		b.WriteString(l + "\r\n")
	}
	data, err := utf16.NewEncoder().Bytes([]byte(b.String()))
	if err != nil {
		return err
	}
	return writeHidden(filepath.Join(dir, iniFileName), data, attrHidden|attrSystem)
}

// writeHidden sustituye un fichero oculto. Windows no deja sobrescribir un
// fichero oculto/sistema a la brava: se le quitan los atributos, se
// reescribe y se le vuelven a poner.
func writeHidden(path string, data []byte, attrs uint32) error {
	if fileExists(path) {
		_ = setAttrs(path, 0, attrHidden|attrSystem|attrReadonly)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return setAttrs(path, attrs, 0)
}

func hasFolderIcon(dir string) bool {
	d := readIni(dir)
	return (d.kind != "" || d.auto != "") && fileExists(filepath.Join(dir, iconFileName))
}

// resolveKind decide con qué tipo se dibuja una carpeta: lo que fijó el
// usuario; si no, el formato de la edición (el que se pasa, o el elegido en
// el análisis); si no, la etiqueta "media" de los ficheros (la escriben
// Picard y nosotros al etiquetar); y si nada, funda plana.
func (a *App) resolveKind(dir, format string) (kind, source, fmtText string) {
	return a.resolveKindWith(dir, format, readIni(dir))
}

// infoTip: lo que el Explorador muestra al pasar el ratón por la carpeta.
func (a *App) infoTip(dir, kind string) string {
	parts := []string{}
	if card, err := a.AlbumCard(dir); err == nil {
		if name := strings.TrimSpace(strings.Trim(card.Artist+" — "+card.Album, " —")); name != "" {
			parts = append(parts, name)
		}
	}
	parts = append(parts, kindLabel[kind])
	switch n := countAudio(dir); {
	case n == 1:
		parts = append(parts, "1 pista")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d pistas", n))
	}
	return strings.Join(parts, " · ")
}

// writeFolderIcon dibuja y deja el icono en la carpeta.
func (a *App) writeFolderIcon(ctx context.Context, dir, kind string, fixed bool, format string) error {
	// Todo lo que lee la carpeta va antes de escribir nada: escribir cambia
	// la fecha de la carpeta y tiraría la caché de la tarjeta.
	tip := a.infoTip(dir, kind)
	cover, err := a.coverImage(dir)
	if err != nil {
		return err
	}
	prev := readIni(dir)
	ico, err := encodeICO(renderIcon(kind, cover))
	if err != nil {
		return err
	}
	if err := writeHidden(filepath.Join(dir, iconFileName), ico, attrHidden); err != nil {
		return err
	}
	if err := writeIni(dir, prev, kind, fixed, format, tip); err != nil {
		return err
	}
	if err := setAttrs(dir, attrReadonly, 0); err != nil {
		return err
	}
	shellNotify(dir)
	return nil
}

// autoIcon actualiza (o crea) el icono tras un cambio en la carpeta, si la
// opción está activa. Los fallos solo se anotan en el registro: el icono
// nunca debe hacer fallar la acción principal.
func (a *App) autoIcon(dir, format string) {
	a.mu.Lock()
	on := a.settings.FolderIcons
	a.mu.Unlock()
	if !on {
		return
	}
	kind, _, fmtText := a.resolveKind(dir, format)
	if err := a.writeFolderIcon(a.ctx, dir, kind, readIni(dir).kind != "", fmtText); err != nil {
		a.libLog("aviso: icono de carpeta: %v", err)
	}
}

// ---- métodos para la ventana ---------------------------------------------

// FolderIcon: estado del icono de una carpeta y vista previa de cómo
// quedaría con el tipo dado ("" = automático). No escribe nada.
func (a *App) FolderIcon(dir, kind string) (folderIconInfo, error) {
	info := folderIconInfo{Exists: hasFolderIcon(dir)}
	if kindLabel[kind] != "" {
		info.Kind, info.Source, info.Fixed = kind, "fijado", true
	} else {
		info.Kind, info.Source, info.Format = a.resolveKind(dir, "")
		info.Fixed = info.Source == "fijado"
	}
	cover, err := a.coverImage(dir)
	if err != nil {
		return info, err
	}
	info.Preview = pngDataURL(renderIcon(info.Kind, cover), 96)
	return info, nil
}

// SetFolderIcon crea el icono con el tipo dado; "" = automático (y deja de
// estar fijado).
func (a *App) SetFolderIcon(dir, kind string) (folderIconInfo, error) {
	fixed := kindLabel[kind] != ""
	var format string
	if !fixed {
		prev := readIni(dir)
		prev.kind = "" // volver a decidir aunque estuviera fijado
		kind, _, format = a.resolveKindWith(dir, "", prev)
	}
	if err := a.writeFolderIcon(a.ctx, dir, kind, fixed, format); err != nil {
		return folderIconInfo{}, err
	}
	a.libLog("Icono de carpeta (%s): %s", kindLabel[kind], filepath.Base(dir))
	return a.FolderIcon(dir, "")
}

// resolveKindWith es resolveKind con un desktop.ini ya leído (o retocado).
func (a *App) resolveKindWith(dir, format string, prev iniData) (kind, source, fmtText string) {
	if k := prev.kind; kindLabel[k] != "" {
		return k, "fijado", prev.format
	}
	if format == "" {
		if an := a.batch.get(dir); an != nil && an.detail != nil {
			format = an.detail.Release.Format
		}
	}
	if k := mediaKind(format); k != "" {
		return k, "edición", format
	}
	if k := mediaKind(prev.format); k != "" {
		return k, "edición", prev.format
	}
	if card, err := a.AlbumCard(dir); err == nil && card.Media != "" {
		m := card.Media
		if k := mediaKind(m); k != "" {
			return k, "etiquetas", m
		}
	}
	return kindDigital, "desconocido", format
}

// RemoveFolderIcon quita el icono y deja la carpeta como una normal. Si el
// desktop.ini tenía cosas de otros programas, se conservan.
func (a *App) RemoveFolderIcon(dir string) error {
	prev := readIni(dir)
	ico := filepath.Join(dir, iconFileName)
	if fileExists(ico) {
		_ = setAttrs(ico, 0, attrHidden|attrSystem|attrReadonly)
		if err := os.Remove(ico); err != nil {
			return err
		}
	}
	ini := filepath.Join(dir, iniFileName)
	if fileExists(ini) {
		_ = setAttrs(ini, 0, attrHidden|attrSystem|attrReadonly)
		if len(prev.shellExtra) == 0 && len(prev.rest) == 0 {
			if err := os.Remove(ini); err != nil {
				return err
			}
			_ = setAttrs(dir, 0, attrReadonly)
		} else {
			var b strings.Builder
			if len(prev.shellExtra) > 0 {
				b.WriteString("[.ShellClassInfo]\r\n" + strings.Join(prev.shellExtra, "\r\n") + "\r\n")
			}
			for _, l := range prev.rest {
				b.WriteString(l + "\r\n")
			}
			data, _ := utf16.NewEncoder().Bytes([]byte(b.String()))
			if err := writeHidden(ini, data, attrHidden|attrSystem); err != nil {
				return err
			}
		}
	}
	shellNotify(dir)
	a.libLog("Icono de carpeta quitado: %s", filepath.Base(dir))
	return nil
}

// ---- toda la colección -----------------------------------------------------

// iconRun controla el lote de iconos (uno a la vez, cancelable).
type iconRun struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// iconProgress es lo que la ventana recibe mientras corre el lote.
type iconProgress struct {
	Done    int  `json:"done"`
	Skipped int  `json:"skipped"`
	Total   int  `json:"total"`
	Running bool `json:"running"`
}

// albumDirs recorre root entero y devuelve las carpetas que tienen audio
// directamente (los discos), a cualquier profundidad. Se saltan las que
// empiezan por "_" o "." (nuestras: _original, _duplicados) y las de sistema.
func albumDirs(ctx context.Context, root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // carpeta ilegible: se salta, no se aborta
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$")) {
			return filepath.SkipDir
		}
		if path != root && countAudio(path) > 0 { // la raíz de la colección no es un disco
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs, err
}

// iconSig resume lo que influye en el icono (tipo, carátula, pistas) para
// no rehacer los que no han cambiado. Solo mira nombres y tamaños: nada de
// lanzar ffmpeg.
func iconSig(dir, kind string) string {
	if kind == kindArtist {
		return artistSig(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(iconVersion + "|" + kind) // la versión del dibujo: si cambia, se rehacen todos
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || !(audioExts[filepath.Ext(name)] || name == "cover.jpg") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "|%s:%d", e.Name(), info.Size())
	}
	return b.String()
}

// MakeFolderIcons pone icono a todos los discos que cuelgan de root (a
// cualquier profundidad), con el tipo automático de cada uno; los que ya lo
// tienen al día se saltan. Devuelve al terminar; el progreso llega por el
// evento "icons" y el registro.
func (a *App) MakeFolderIcons(root string) (iconProgress, error) {
	a.icons.mu.Lock()
	if a.icons.cancel != nil {
		a.icons.mu.Unlock()
		return iconProgress{}, fmt.Errorf("ya hay un lote de iconos en marcha")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.icons.cancel = cancel
	a.icons.mu.Unlock()
	defer func() {
		cancel()
		a.icons.mu.Lock()
		a.icons.cancel = nil
		a.icons.mu.Unlock()
	}()

	a.libLog("Iconos de carpeta: buscando discos en %s y todas sus subcarpetas...", root)
	dirs, err := albumDirs(ctx, root)
	if err != nil {
		return iconProgress{}, err
	}
	artists := artistDirs(root, dirs)
	p := iconProgress{Total: len(dirs) + len(artists), Running: true}
	a.emit("icons", p)
	a.libLog("Iconos de carpeta: %d discos y %d artistas", len(dirs), len(artists))

	// Varios a la vez: lo que tarda es lanzar ffmpeg/ffprobe por carpeta.
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		jobs = make(chan string)
	)
	report := func(skipped bool) {
		mu.Lock()
		if skipped {
			p.Skipped++
		} else {
			p.Done++
		}
		n := p.Done + p.Skipped
		cur := p
		mu.Unlock()
		if n%10 == 0 || n == p.Total {
			a.emit("icons", cur)
		}
		if n%100 == 0 {
			a.libLog("Iconos: %d/%d (%d ya estaban al día)", n, cur.Total, cur.Skipped)
		}
	}
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range jobs {
				if ctx.Err() != nil {
					continue
				}
				prev := readIni(dir)
				kind, _, format := a.resolveKindWith(dir, "", prev)
				if sig := iconSig(dir, kind); sig != "" && sig == prev.sig && fileExists(filepath.Join(dir, iconFileName)) {
					report(true)
					continue
				}
				if err := a.writeFolderIcon(ctx, dir, kind, prev.kind != "", format); err != nil {
					if ctx.Err() == nil {
						a.libLog("aviso: %s: %v", filepath.Base(dir), err)
					}
					continue
				}
				a.libLog("icono %s: %s", kindLabel[kind], relTo(root, dir))
				report(false)
			}
		}()
	}
	for _, dir := range dirs {
		select {
		case jobs <- dir:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	// Las carpetas de artista, de una en una: buscar la imagen es hablar con
	// MusicBrainz (1 petición por segundo) y las demás fuentes.
	for _, dir := range artists {
		if ctx.Err() != nil {
			break
		}
		skipped, err := a.refreshArtistIcon(ctx, dir)
		if err != nil {
			if ctx.Err() == nil {
				a.libLog("aviso: artista %s: %v", filepath.Base(dir), err)
			}
			continue
		}
		if !skipped {
			a.libLog("icono artista: %s", relTo(root, dir))
		}
		report(skipped)
	}

	p.Running = false
	a.emit("icons", p)
	if p.Done > 0 {
		shellRefreshIcons() // que el Explorador suelte los iconos cacheados
	}
	if ctx.Err() != nil {
		a.libLog("Iconos: cancelado (%d hechos, %d ya estaban)", p.Done, p.Skipped)
	} else {
		a.libLog("Iconos de carpeta: %d hechos, %d ya estaban al día, de %d. Si el Explorador no los muestra aún, pulsa F5 en la carpeta.", p.Done, p.Skipped, p.Total)
	}
	return p, nil
}

// CancelIcons detiene el lote de iconos.
func (a *App) CancelIcons() {
	a.icons.mu.Lock()
	defer a.icons.mu.Unlock()
	if a.icons.cancel != nil {
		a.icons.cancel()
	}
}

// afterDownload: las descargas que han creado carpeta (listas, álbumes de
// Bandcamp) reciben su icono de funda sin que haya que hacer nada.
func (a *App) afterDownload(paths []string) {
	a.mu.Lock()
	on, out := a.settings.FolderIcons, a.settings.OutDir
	a.mu.Unlock()
	if !on {
		return
	}
	seen := map[string]bool{}
	for _, p := range paths {
		dir := filepath.Dir(p)
		if seen[dir] || strings.EqualFold(dir, out) {
			continue // sueltas en la carpeta de descargas: no son un disco
		}
		seen[dir] = true
		a.autoIcon(dir, "Digital Media")
	}
}

// fixIconAttrs vuelve a poner los atributos del icono de carpeta (se
// pierden si la carpeta se copió en vez de renombrarse).
func fixIconAttrs(dir string) {
	if !hasFolderIcon(dir) {
		return
	}
	_ = setAttrs(filepath.Join(dir, iniFileName), attrHidden|attrSystem, 0)
	_ = setAttrs(filepath.Join(dir, iconFileName), attrHidden, 0)
	_ = setAttrs(dir, attrReadonly, 0)
}

// relTo: ruta relativa a root para el registro (o la absoluta si no cuelga).
func relTo(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}
