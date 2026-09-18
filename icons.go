package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decodificadores de carátulas y logos
	_ "image/jpeg"
	"image/png"
	"math"
	"os/exec"
	"regexp"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
	_ "golang.org/x/image/webp"
)

// Iconos de carpeta para el Explorador de Windows: cada disco se dibuja como
// lo que es —un CD, un vinilo asomando de su funda, una cinta— con su
// carátula, y se deja como icono de la carpeta (desktop.ini + folder.ico).
// El dibujo es geometría pura (círculos y rectángulos redondeados
// rasterizados con antialiasing) rellena con la carátula o con colores, así
// que no hace falta ninguna plantilla ni fuente.

const iconSize = 256

// iconVersion cambia cuando cambia el dibujo, para que el lote rehaga los
// iconos ya puestos. Los de artista llevan la suya (artistIconVersion): así
// retocar uno de los dos dibujos no obliga a rehacer el otro, que en una
// colección grande son miles de carátulas releídas para nada.
const iconVersion = "5"

// Tipos de icono (el "kind" que se guarda en desktop.ini).
const (
	kindCD       = "cd"
	kindVinyl    = "vinyl"
	kindCassette = "cassette"
	kindDigital  = "digital" // funda plana: descargas, o formato desconocido
)

var kindLabel = map[string]string{kindCD: "CD", kindVinyl: "Vinilo", kindCassette: "Cassette", kindDigital: "Digital"}

var (
	vinylInch = regexp.MustCompile(`\b(7|10|12)\s*("|''|in\b|inch)`)
	lpWord    = regexp.MustCompile(`\b(lp|ep)\b`)
)

// mediaKind traduce el formato de una edición, tal como lo dan las fuentes
// (MusicBrainz "12\" Vinyl", "CD+CD", "Cassette", "Digital Media"; Discogs
// "Vinyl, LP, Album", "Cass", "File"), a un tipo de icono. "" si no se sabe.
func mediaKind(format string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	switch {
	case f == "":
		return ""
	case strings.Contains(f, "vinyl"), strings.Contains(f, "vinilo"), vinylInch.MatchString(f), lpWord.MatchString(f):
		return kindVinyl
	case strings.Contains(f, "cass"), strings.Contains(f, "tape"), strings.Contains(f, "cinta"), f == "mc":
		return kindCassette
	case strings.Contains(f, "cd"):
		return kindCD
	case strings.Contains(f, "digital"), strings.Contains(f, "file"), strings.Contains(f, "mp3"), strings.Contains(f, "flac"):
		return kindDigital
	}
	return ""
}

// ---- rasterizador --------------------------------------------------------

// shape acumula un camino (varias figuras) y lo rellena de una vez. Una
// figura trazada al revés hace un agujero (regla del número de vueltas).
type shape struct{ z *vector.Rasterizer }

func newShape() *shape { return &shape{vector.NewRasterizer(iconSize, iconSize)} }

const kappa = 0.552284749831 // círculo con cuatro curvas de Bézier

func (s *shape) circle(cx, cy, r float64, hole bool) {
	z, k := s.z, kappa*r
	x, y := float32(cx), float32(cy)
	R, K := float32(r), float32(k)
	z.MoveTo(x+R, y)
	if !hole {
		z.CubeTo(x+R, y+K, x+K, y+R, x, y+R)
		z.CubeTo(x-K, y+R, x-R, y+K, x-R, y)
		z.CubeTo(x-R, y-K, x-K, y-R, x, y-R)
		z.CubeTo(x+K, y-R, x+R, y-K, x+R, y)
	} else {
		z.CubeTo(x+R, y-K, x+K, y-R, x, y-R)
		z.CubeTo(x-K, y-R, x-R, y-K, x-R, y)
		z.CubeTo(x-R, y+K, x-K, y+R, x, y+R)
		z.CubeTo(x+K, y+R, x+R, y+K, x+R, y)
	}
	z.ClosePath()
}

// ring: corona circular entre dos radios.
func (s *shape) ring(cx, cy, outer, inner float64) {
	s.circle(cx, cy, outer, false)
	if inner > 0 {
		s.circle(cx, cy, inner, true)
	}
}

func (s *shape) roundRect(x, y, w, h, r float64) {
	s.roundRectDir(x, y, w, h, r, false)
}

// roundRectDir traza el rectángulo redondeado; al revés (hole) recorta un
// agujero en la figura anterior, p. ej. para dibujar solo un marco.
func (s *shape) roundRectDir(x, y, w, h, r float64, hole bool) {
	z := s.z
	k := float32(r * (1 - kappa))
	X, Y, W, H, R := float32(x), float32(y), float32(w), float32(h), float32(r)
	if !hole {
		z.MoveTo(X+R, Y)
		z.LineTo(X+W-R, Y)
		z.CubeTo(X+W-k, Y, X+W, Y+k, X+W, Y+R)
		z.LineTo(X+W, Y+H-R)
		z.CubeTo(X+W, Y+H-k, X+W-k, Y+H, X+W-R, Y+H)
		z.LineTo(X+R, Y+H)
		z.CubeTo(X+k, Y+H, X, Y+H-k, X, Y+H-R)
		z.LineTo(X, Y+R)
		z.CubeTo(X, Y+k, X+k, Y, X+R, Y)
	} else {
		z.MoveTo(X+R, Y)
		z.CubeTo(X+k, Y, X, Y+k, X, Y+R)
		z.LineTo(X, Y+H-R)
		z.CubeTo(X, Y+H-k, X+k, Y+H, X+R, Y+H)
		z.LineTo(X+W-R, Y+H)
		z.CubeTo(X+W-k, Y+H, X+W, Y+H-k, X+W, Y+H-R)
		z.LineTo(X+W, Y+R)
		z.CubeTo(X+W, Y+k, X+W-k, Y, X+W-R, Y)
	}
	z.ClosePath()
}

func (s *shape) polygon(pts ...float64) {
	z := s.z
	z.MoveTo(float32(pts[0]), float32(pts[1]))
	for i := 2; i+1 < len(pts); i += 2 {
		z.LineTo(float32(pts[i]), float32(pts[i+1]))
	}
	z.ClosePath()
}

// fill pinta src a través de la figura acumulada y la vacía. src puede ser
// un color, un degradado o la carátula ya escalada y situada.
func (s *shape) fill(dst *image.RGBA, src image.Image) {
	s.z.DrawOp = draw.Over
	s.z.Draw(dst, dst.Bounds(), src, image.Point{})
	s.z.Reset(iconSize, iconSize)
}

func rgba(r, g, b, a uint8) *image.Uniform { return image.NewUniform(color.NRGBA{r, g, b, a}) }

// radial es un degradado circular (brillo) usable como imagen fuente.
type radial struct {
	cx, cy, r float64
	in, out   color.NRGBA
}

func (g radial) ColorModel() color.Model { return color.NRGBAModel }
func (g radial) Bounds() image.Rectangle { return image.Rect(-1<<20, -1<<20, 1<<20, 1<<20) }
func (g radial) At(x, y int) color.Color {
	t := math.Hypot(float64(x)+0.5-g.cx, float64(y)+0.5-g.cy) / g.r
	t = math.Min(1, t)
	mix := func(a, b uint8) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }
	return color.NRGBA{mix(g.in.R, g.out.R), mix(g.in.G, g.out.G), mix(g.in.B, g.out.B), mix(g.in.A, g.out.A)}
}

func gloss(cx, cy, r float64, alpha uint8) radial {
	return radial{cx, cy, r, color.NRGBA{255, 255, 255, alpha}, color.NRGBA{255, 255, 255, 0}}
}

// fitCover escala la carátula (recortada al centro para respetar la
// proporción) y la deja situada en (x,y) del lienzo, lista para usarse como
// fuente de un relleno.
func fitCover(src image.Image, x, y, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(x, y, x+w, y+h))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw*h > sh*w { // más ancha que el destino: recortar los lados
		cw := sh * w / h
		sb.Min.X += (sw - cw) / 2
		sb.Max.X = sb.Min.X + cw
	} else {
		ch := sw * h / w
		sb.Min.Y += (sh - ch) / 2
		sb.Max.Y = sb.Min.Y + ch
	}
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Src, nil)
	return dst
}

// placeholder: "carátula" gris para discos sin imagen.
func placeholder() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(img, img.Bounds(), rgba(58, 60, 66, 255), image.Point{}, draw.Src)
	s := &shape{vector.NewRasterizer(64, 64)}
	s.circle(32, 32, 14, false)
	s.z.Draw(img, img.Bounds(), rgba(120, 124, 134, 255), image.Point{})
	return img
}

// ---- los cuatro dibujos --------------------------------------------------

// renderIcon dibuja el icono de 256×256 (fondo transparente).
func renderIcon(kind string, cover image.Image) *image.RGBA {
	if cover == nil {
		cover = placeholder()
	}
	dst := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	switch kind {
	case kindVinyl:
		drawVinyl(dst, cover)
	case kindCassette:
		drawCassette(dst, cover)
	case kindCD:
		drawCD(dst, cover)
	default:
		drawSleeve(dst, cover)
	}
	return dst
}

// Funda plana: la carátula con las esquinas redondeadas y una sombra.
func drawSleeve(dst *image.RGBA, cover image.Image) {
	s := newShape()
	s.roundRect(20, 24, 224, 224, 10)
	s.fill(dst, rgba(0, 0, 0, 90))
	s.roundRect(16, 16, 224, 224, 10)
	s.fill(dst, fitCover(cover, 16, 16, 224, 224))
	s.roundRect(16, 16, 224, 224, 10)
	s.fill(dst, gloss(60, 40, 260, 40))
}

// CD: el disco con la carátula impresa, brillo y el aro transparente.
func drawCD(dst *image.RGBA, cover image.Image) {
	const cx, cy, R = 128, 128, 118
	s := newShape()
	s.circle(cx+3, cy+5, R, false)
	s.fill(dst, rgba(0, 0, 0, 100))
	s.ring(cx, cy, R, 13)
	s.fill(dst, fitCover(cover, cx-R, cy-R, 2*R, 2*R))
	s.ring(cx, cy, R, 13)
	s.fill(dst, gloss(84, 76, 190, 120))
	s.ring(cx, cy, R, R-3)
	s.fill(dst, rgba(0, 0, 0, 70)) // canto
	s.ring(cx, cy, 36, 13)
	s.fill(dst, rgba(228, 230, 236, 200)) // aro de plástico
	s.ring(cx, cy, 36, 34)
	s.fill(dst, rgba(0, 0, 0, 50))
	s.ring(cx, cy, 15, 13)
	s.fill(dst, rgba(0, 0, 0, 60))
}

// Vinilo: el disco negro con surcos asoma por la derecha de la funda.
func drawVinyl(dst *image.RGBA, cover image.Image) {
	const cx, cy, R = 170, 128, 86
	s := newShape()
	s.circle(cx+2, cy+3, R, false)
	s.fill(dst, rgba(0, 0, 0, 100))
	s.ring(cx, cy, R, 4)
	s.fill(dst, rgba(22, 22, 24, 255))
	for r := 40.0; r <= R-4; r += 5 {
		s.ring(cx, cy, r, r-1)
	}
	s.fill(dst, rgba(255, 255, 255, 20)) // surcos
	s.ring(cx, cy, R, 4)
	s.fill(dst, gloss(120, 80, 130, 45))
	s.ring(cx, cy, 34, 4)
	s.fill(dst, fitCover(cover, cx-34, cy-34, 68, 68)) // galleta
	s.ring(cx, cy, 34, 33)
	s.fill(dst, rgba(0, 0, 0, 90))
	// Funda delante.
	s.roundRect(9, 41, 184, 184, 4)
	s.fill(dst, rgba(0, 0, 0, 110))
	s.roundRect(4, 36, 184, 184, 4)
	s.fill(dst, fitCover(cover, 4, 36, 184, 184))
	s.roundRect(4, 36, 184, 184, 4)
	s.fill(dst, gloss(40, 60, 220, 35))
}

// Cassette: carcasa oscura, la carátula como etiqueta, ventana con las dos
// bobinas y el frontal inferior.
func drawCassette(dst *image.RGBA, cover image.Image) {
	s := newShape()
	s.roundRect(16, 48, 232, 168, 14)
	s.fill(dst, rgba(0, 0, 0, 100))
	s.roundRect(12, 44, 232, 168, 14)
	s.fill(dst, rgba(40, 40, 44, 255))
	s.roundRect(12, 44, 232, 168, 14)
	s.fill(dst, gloss(50, 50, 260, 40))
	// Etiqueta con la carátula (franja central de la imagen).
	s.roundRect(26, 56, 204, 100, 6)
	s.fill(dst, fitCover(cover, 26, 56, 204, 100))
	// Ventana.
	s.roundRect(54, 102, 148, 48, 10)
	s.fill(dst, rgba(12, 12, 14, 255))
	s.roundRect(58, 106, 140, 40, 8)
	s.fill(dst, rgba(28, 28, 30, 255))
	for _, x := range []float64{96, 160} {
		s.circle(x, 126, 16, false)
		s.fill(dst, rgba(58, 42, 28, 255)) // cinta enrollada
		s.ring(x, 126, 11, 5)
		s.fill(dst, rgba(232, 232, 232, 255)) // bobina
	}
	// Frontal inferior con los agujeros de arrastre.
	s.polygon(52, 212, 204, 212, 188, 172, 68, 172)
	s.fill(dst, rgba(26, 26, 28, 255))
	for _, x := range []float64{84, 128, 172} {
		s.circle(x, 194, 5, false)
	}
	s.fill(dst, rgba(8, 8, 8, 255))
	for _, p := range [][2]float64{{24, 56}, {232, 56}, {24, 200}, {232, 200}} {
		s.circle(p[0], p[1], 4, false)
	}
	s.fill(dst, rgba(80, 80, 86, 255)) // tornillos
}

// ---- fichero .ico ----------------------------------------------------------

// encodeICO genera un .ico con el icono a varios tamaños: 256 en PNG (lo
// admite Windows desde Vista) y el resto en mapa de bits clásico, que es lo
// que entiende todo el mundo para los tamaños pequeños.
func encodeICO(img *image.RGBA) ([]byte, error) {
	sizes := []int{256, 128, 64, 48, 32, 16}
	var entries [][]byte
	for _, size := range sizes {
		scaled := img
		if size != iconSize {
			scaled = image.NewRGBA(image.Rect(0, 0, size, size))
			xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), img, img.Bounds(), draw.Src, nil)
		}
		if size == iconSize {
			// PNG guarda el alfa sin premultiplicar, al revés que image.RGBA:
			// sin convertir, la sombra y los bordes suavizados salen más
			// oscuros de la cuenta y no cuadran con los tamaños pequeños.
			straight := image.NewNRGBA(scaled.Bounds())
			draw.Draw(straight, straight.Bounds(), scaled, scaled.Bounds().Min, draw.Src)
			var buf bytes.Buffer
			if err := png.Encode(&buf, straight); err != nil {
				return nil, err
			}
			entries = append(entries, buf.Bytes())
		} else {
			entries = append(entries, icoBitmap(scaled))
		}
	}
	var out bytes.Buffer
	le := binary.LittleEndian
	w := func(v any) { _ = binary.Write(&out, le, v) }
	w(uint16(0))
	w(uint16(1)) // 1 = icono
	w(uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		dim := uint8(size)
		if size == 256 {
			dim = 0 // 0 significa 256
		}
		w(dim)
		w(dim)
		w(uint8(0)) // paleta
		w(uint8(0))
		w(uint16(1))  // planos
		w(uint16(32)) // bits por píxel
		w(uint32(len(entries[i])))
		w(uint32(offset))
		offset += len(entries[i])
	}
	for _, e := range entries {
		out.Write(e)
	}
	return out.Bytes(), nil
}

// icoBitmap: entrada BMP de 32 bits (BGRA, filas de abajo arriba) seguida
// de la máscara AND de 1 bit, como manda el formato.
func icoBitmap(img *image.RGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	maskRow := ((w + 31) / 32) * 4
	var out bytes.Buffer
	le := binary.LittleEndian
	put := func(v any) { _ = binary.Write(&out, le, v) }
	put(uint32(40))
	put(int32(w))
	put(int32(2 * h)) // XOR + AND
	put(uint16(1))
	put(uint16(32))
	put(uint32(0))
	put(uint32(w*h*4 + maskRow*h))
	put(int32(0))
	put(int32(0))
	put(uint32(0))
	put(uint32(0))
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y) // premultiplicado: deshacerlo
			r, g, b, a := c.R, c.G, c.B, c.A
			if a > 0 && a < 255 {
				r = uint8(int(r) * 255 / int(a))
				g = uint8(int(g) * 255 / int(a))
				b = uint8(int(b) * 255 / int(a))
			}
			out.Write([]byte{b, g, r, a})
		}
	}
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, maskRow)
		for x := 0; x < w; x++ {
			if img.RGBAAt(x, y).A == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		out.Write(row)
	}
	return out.Bytes()
}

// ---- carátula de la carpeta como imagen --------------------------------

// coverImage decodifica la carátula de la carpeta (cover.jpg o la incrustada)
// a partir de la caché de la cuadrícula, para no volver a lanzar ffmpeg.
// nil (sin error) si no hay.
func (a *App) coverImage(dir string) (image.Image, error) {
	url, err := a.AlbumCover(dir)
	if err != nil || url == "" {
		return nil, err
	}
	_, b64, _ := strings.Cut(url, ",")
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	ctx := a.ctx
	if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		return img, nil
	}
	// Formato raro: que lo convierta ffmpeg a PNG.
	out, err := ffmpegToPNG(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("no pude leer la carátula")
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	return img, err
}

// ffmpegToPNG convierte una imagen en cualquier formato que entienda ffmpeg
// a PNG (para las carátulas raras que no decodifica Go).
func ffmpegToPNG(ctx context.Context, data []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, toolPath("ffmpeg.exe"), "-v", "error", "-i", "-", "-f", "image2pipe", "-c:v", "png", "-")
	hideWindow(cmd)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Output()
}

// pngDataURL: vista previa para la ventana, reducida a `size` píxeles.
func pngDataURL(img *image.RGBA, size int) string {
	small := img
	if size != img.Bounds().Dx() {
		small = image.NewRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, small); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
