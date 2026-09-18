// Prepara la imagen de la gramola: mide el cristal y el panel de tiras, deja
// transparente el fondo negro y el interior del cristal, y escribe el PNG
// final junto con las coordenadas (en % del ancho/alto) para la interfaz.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strconv"
)

func lum(c color.Color) int {
	r, g, b, _ := c.RGBA()
	return int((299*r + 587*g + 114*b) / 1000 >> 8)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "deck":
			deck(os.Args[2:])
			return
		case "case":
			caseTemplate(os.Args[2:])
			return
		case "tape":
			tapeTemplate(os.Args[2:])
			return
		case "gramo":
			gramoTemplate(os.Args[2:])
			return
		case "record":
			recordTemplate(os.Args[2:])
			return
		case "logo":
			logoTemplate(os.Args[2:])
			return
		case "sleeve":
			sleeveTemplate(os.Args[2:])
			return
		}
	}
	in, out := os.Args[1], os.Args[2]
	f, err := os.Open(in)
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	b := src.Bounds()
	W, H := b.Dx(), b.Dy()
	fmt.Println("tamaño", W, H)

	// 1. Panel de tiras: la mayor zona crema contigua en la franja central.
	isCream := func(x, y int) bool {
		r, g, bl, _ := src.At(x, y).RGBA()
		r, g, bl = r>>8, g>>8, bl>>8
		return r > 190 && g > 170 && bl > 130 && r-bl < 90 && r-bl > 15
	}
	// Buscamos por filas: la fila con más píxeles crema entre x=25%..75%.
	x0, x1 := W/4, 3*W/4
	bestY, bestN := 0, 0
	for y := H * 25 / 100; y < H*50/100; y++ {
		n := 0
		for x := x0; x < x1; x++ {
			if isCream(x, y) {
				n++
			}
		}
		if n > bestN {
			bestN, bestY = n, y
		}
	}
	// Extender desde esa fila hacia arriba y abajo mientras siga habiendo crema.
	rowCream := func(y int) (int, int, bool) {
		l, r := -1, -1
		for x := x0; x < x1; x++ {
			if isCream(x, y) {
				if l < 0 {
					l = x
				}
				r = x
			}
		}
		return l, r, r-l > bestN*7/10
	}
	top, bottom := bestY, bestY
	for y := bestY; y > 0; y-- {
		if _, _, ok := rowCream(y); !ok {
			break
		}
		top = y
	}
	for y := bestY; y < H; y++ {
		if _, _, ok := rowCream(y); !ok {
			break
		}
		bottom = y
	}
	pl, pr, _ := rowCream((top + bottom) / 2)
	fmt.Printf("panel de tiras: x %d..%d  y %d..%d\n", pl, pr, top, bottom)

	// 2. Cristal: la zona oscura interior del arco, por encima del panel.
	// Columna central: desde el panel hacia arriba, oscuro hasta el marco.
	cx := W / 2
	glassBottom, glassTop := 0, 0
	for y := top - 1; y > 0; y-- {
		if lum(src.At(cx, y)) < 15 {
			glassBottom = y
			break
		}
	}
	for y := glassBottom; y > 0; y-- {
		if lum(src.At(cx, y)) >= 15 {
			glassTop = y + 1
			break
		}
	}
	// Anchura del cristal en su parte baja (la recta del arco).
	yMid := glassBottom - 10
	gl, gr := cx, cx
	for x := cx; x > 0; x-- {
		if lum(src.At(x, yMid)) >= 15 {
			gl = x + 1
			break
		}
	}
	for x := cx; x < W; x++ {
		if lum(src.At(x, yMid)) >= 15 {
			gr = x - 1
			break
		}
	}
	fmt.Printf("cristal: x %d..%d  y %d..%d\n", gl, gr, glassTop, glassBottom)

	// 3. Salida: fondo negro (conectado al borde) y cristal transparentes.
	dst := image.NewNRGBA(b)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	// Fondo: relleno por inundación desde las esquinas sobre píxeles muy oscuros.
	visited := make([]bool, W*H)
	stack := []image.Point{{0, 0}, {W - 1, 0}, {0, H - 1}, {W - 1, H - 1}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if p.X < 0 || p.Y < 0 || p.X >= W || p.Y >= H || visited[p.Y*W+p.X] {
			continue
		}
		visited[p.Y*W+p.X] = true
		if lum(src.At(p.X, p.Y)) > 22 {
			continue
		}
		dst.SetNRGBA(p.X, p.Y, color.NRGBA{0, 0, 0, 0})
		stack = append(stack, image.Point{p.X + 1, p.Y}, image.Point{p.X - 1, p.Y}, image.Point{p.X, p.Y + 1}, image.Point{p.X, p.Y - 1})
	}
	// Suavizar el borde del fondo: lo oscuro pegado al fondo (la sombra bajo
	// los pies, el halo del recorte) se atenúa según su luminancia.
	for pass := 0; pass < 3; pass++ {
		for y := 1; y < H-1; y++ {
			for x := 1; x < W-1; x++ {
				c := dst.NRGBAAt(x, y)
				if c.A == 0 {
					continue
				}
				l := lum(src.At(x, y))
				if l >= 60 {
					continue
				}
				near := dst.NRGBAAt(x-1, y).A < 128 || dst.NRGBAAt(x+1, y).A < 128 || dst.NRGBAAt(x, y-1).A < 128 || dst.NRGBAAt(x, y+1).A < 128
				if near {
					c.A = uint8(l * 255 / 60)
					dst.SetNRGBA(x, y, c)
				}
			}
		}
	}
	// Cristal: todo lo que esté dentro del arco (semicírculo sobre el rectángulo)
	// se vuelve transparente, con un degradado en los 3 px del borde.
	gcx := float64(gl+gr) / 2
	rad := float64(gr-gl) / 2
	arcCy := float64(glassTop) + rad // centro del semicírculo
	inside := func(x, y int) float64 {
		fx, fy := float64(x)+0.5, float64(y)+0.5
		if fy >= arcCy {
			d := rad - abs(fx-gcx)
			if fy > float64(glassBottom)+0.5 {
				return 0
			}
			return clamp(d)
		}
		dx, dy := fx-gcx, fy-arcCy
		d := rad - sqrt(dx*dx+dy*dy)
		return clamp(d)
	}
	for y := glassTop - 2; y <= glassBottom; y++ {
		for x := gl - 2; x <= gr+2; x++ {
			if k := inside(x, y); k > 0 {
				c := dst.NRGBAAt(x, y)
				c.A = uint8(float64(c.A) * (1 - k))
				dst.SetNRGBA(x, y, c)
			}
		}
	}
	// 4. A juego con la interfaz: las luces (ámbar y rojo, saturadas y
	// claras) pasan al tono del acento de la app, y la madera y los cremas se
	// enfrían un poco hacia los grises de la ventana. Se pasa el tono del
	// acento en grados como tercer argumento (160 = menta); sin él, no se toca.
	if len(os.Args) > 3 {
		hue, _ := strconv.ParseFloat(os.Args[3], 64)
		tint(dst, hue)
	}
	o, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	if err := png.Encode(o, dst); err != nil {
		panic(err)
	}
	o.Close()
	pct := func(v, total int) string { return fmt.Sprintf("%.2f%%", float64(v)*100/float64(total)) }
	fmt.Println("CSS (porcentajes del mueble):")
	fmt.Printf("  cristal: left %s top %s width %s height %s (arco de radio %s del ancho)\n", pct(gl, W), pct(glassTop, H), pct(gr-gl, W), pct(glassBottom-glassTop, H), pct(gr-gl, 2*W))
	fmt.Printf("  panel:   left %s top %s width %s height %s\n", pct(pl, W), pct(top, H), pct(pr-pl, W), pct(bottom-top, H))
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
func clamp(d float64) float64 {
	// borde suave de 3 px
	switch {
	case d <= 0:
		return 0
	case d >= 3:
		return 1
	}
	return d / 3
}
func sqrt(v float64) float64 {
	// Newton, suficiente aquí
	if v <= 0 {
		return 0
	}
	x := v
	for i := 0; i < 20; i++ {
		x = (x + v/x) / 2
	}
	return x
}

// tint recolorea las luces al tono dado y enfría el resto.
func tint(img *image.NRGBA, hue float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			h, s, v := rgbToHSV(c.R, c.G, c.B)
			// Luces: ámbar/naranja/rojo, saturado y claro. Peso suave para que
			// los bordes no corten.
			hueW := 0.0
			if h < 75 || h > 335 {
				hueW = 1
			} else if h < 90 {
				hueW = (90 - h) / 15
			}
			w := hueW * ramp(s, 0.35, 0.55) * ramp(v, 0.42, 0.6)
			if w > 0 {
				// Se gira el tono en bloque (ámbar -> acento) para que los rojos,
				// los brillos y los degradados del tubo sigan siendo coherentes.
				// Los tonos del tubo (rojo 0°, ámbar 38°, amarillo 60°) se aprietan
				// alrededor del acento para que todo quede en la misma familia.
				hc := h
				if h > 335 {
					hc = h - 360
				}
				nh := hue + (hc-38)*0.35
				for nh < 0 {
					nh += 360
				}
				for nh >= 360 {
					nh -= 360
				}
				r, g, bl := hsvToRGB(nh, s*0.7, v)
				c.R = mix(c.R, r, w)
				c.G = mix(c.G, g, w)
				c.B = mix(c.B, bl, w)
			} else {
				// Madera y cremas: menos saturación y algo menos de luz, hacia los
				// grises de la ventana (nogal ahumado).
				r, g, bl := hsvToRGB(h, s*0.62, v*0.94)
				c.R, c.G, c.B = r, g, bl
			}
			img.SetNRGBA(x, y, c)
		}
	}
}

func ramp(v, lo, hi float64) float64 {
	if v <= lo {
		return 0
	}
	if v >= hi {
		return 1
	}
	return (v - lo) / (hi - lo)
}

func mix(a, b uint8, w float64) uint8 { return uint8(float64(a)*(1-w) + float64(b)*w + 0.5) }

func rgbToHSV(r8, g8, b8 uint8) (h, s, v float64) {
	r, g, b := float64(r8)/255, float64(g8)/255, float64(b8)/255
	mx, mn := max(r, g, b), min(r, g, b)
	v = mx
	d := mx - mn
	if mx > 0 {
		s = d / mx
	}
	if d == 0 {
		return 0, s, v
	}
	switch mx {
	case r:
		h = 60 * ((g - b) / d)
	case g:
		h = 60 * ((b-r)/d + 2)
	default:
		h = 60 * ((r-g)/d + 4)
	}
	if h < 0 {
		h += 360
	}
	return
}

func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
	c := v * s
	hh := h / 60
	x := c * (1 - abs(float64(int(hh)%2)+(hh-float64(int(hh)))-1))
	var r, g, b float64
	switch {
	case hh < 1:
		r, g, b = c, x, 0
	case hh < 2:
		r, g, b = x, c, 0
	case hh < 3:
		r, g, b = 0, c, x
	case hh < 4:
		r, g, b = 0, x, c
	case hh < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := v - c
	return uint8((r+m)*255 + 0.5), uint8((g+m)*255 + 0.5), uint8((b+m)*255 + 0.5)
}

// ---- la pletina de casete ---------------------------------------------------
//
//	go run ./tools/cabinet deck assets/deck.png frontend/deck/deck.png [tono]
//
// Deja transparente el fondo negro y semitransparente el interior del hueco
// de la casete (medido a mano sobre la foto: la cinta se dibuja detrás y se
// ve a través de la puerta, conservando sus reflejos), y aplica el mismo
// baño de color del acento.
func deck(args []string) {
	in, out := args[0], args[1]
	f, err := os.Open(in)
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	b := src.Bounds()
	W, H := b.Dx(), b.Dy()
	dst := image.NewNRGBA(b)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	// Fondo: inundación desde las esquinas sobre lo muy oscuro.
	visited := make([]bool, W*H)
	stack := []image.Point{{0, 0}, {W - 1, 0}, {0, H - 1}, {W - 1, H - 1}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if p.X < 0 || p.Y < 0 || p.X >= W || p.Y >= H || visited[p.Y*W+p.X] {
			continue
		}
		visited[p.Y*W+p.X] = true
		if lum(src.At(p.X, p.Y)) > 12 {
			continue
		}
		dst.SetNRGBA(p.X, p.Y, color.NRGBA{0, 0, 0, 0})
		stack = append(stack, image.Point{p.X + 1, p.Y}, image.Point{p.X - 1, p.Y}, image.Point{p.X, p.Y + 1}, image.Point{p.X, p.Y - 1})
	}
	// El hueco de la casete (proporciones de la foto de 2172×724).
	wx0, wy0, wx1, wy1 := W*225/2172, H*150/724, W*850/2172, H*450/724
	for y := wy0; y < wy1; y++ {
		for x := wx0; x < wx1; x++ {
			c := dst.NRGBAAt(x, y)
			// Los reflejos claros de la puerta se quedan; lo oscuro se abre.
			l := lum(src.At(x, y))
			keep := 0.28 + 0.72*ramp(float64(l), 40, 140)
			// Borde suave de 12 px.
			e := float64(min(x-wx0, wx1-1-x, y-wy0, wy1-1-y))
			if e < 12 {
				keep = keep + (1-keep)*(1-e/12)
			}
			c.A = uint8(float64(c.A) * keep)
			dst.SetNRGBA(x, y, c)
		}
	}
	if len(args) > 2 {
		hue, _ := strconv.ParseFloat(args[2], 64)
		tint(dst, hue)
	}
	o, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	if err := png.Encode(o, dst); err != nil {
		panic(err)
	}
	o.Close()
	pct := func(v, total int) string { return fmt.Sprintf("%.2f%%", float64(v)*100/float64(total)) }
	fmt.Println("tamaño", W, H)
	fmt.Printf("hueco: left %s top %s width %s height %s\n", pct(wx0, W), pct(wy0, H), pct(wx1-wx0, W), pct(wy1-wy0, H))
}

// grade acerca la foto a la luz de la interfaz sin cambiar los materiales
// (recolorearlos a lo bruto deja el latón y la caoba manchados): las
// sombras se mezclan con el gris azulado del fondo de la app y los brillos
// con el menta del acento, cada uno llevado a la luminancia del píxel para
// que solo cambie el matiz, como si la iluminara la propia ventana; y todo
// queda un poco menos saturado; k gradúa la fuerza (1 = del todo). (Girar
// el tono en HSV hacia el azul manda
// la caoba al magenta por el arco corto: por eso se mezcla en RGB.)
func grade(img *image.NRGBA, k float64) {
	shadow := [3]float64{23, 26, 33}    // #171a21, el panel de la interfaz
	accent := [3]float64{110, 231, 183} // #6ee7b7
	lumOf := func(c [3]float64) float64 { return 0.299*c[0] + 0.587*c[1] + 0.114*c[2] }
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			h, s, v := rgbToHSV(c.R, c.G, c.B)
			r8, g8, b8 := hsvToRGB(h, s*(1-0.15*k), v)
			p := [3]float64{float64(r8), float64(g8), float64(b8)}
			l := lumOf(p)
			ws := (1 - ramp(l/255, 0.12, 0.45)) * 0.55 * k
			wh := ramp(l/255, 0.72, 0.97) * 0.45 * k
			for _, t := range []struct {
				col [3]float64
				w   float64
			}{{shadow, ws}, {accent, wh}} {
				if t.w <= 0 {
					continue
				}
				k := l / math.Max(1, lumOf(t.col)) // el color de la luz, a la luminancia del píxel
				for i := range p {
					p[i] = p[i]*(1-t.w) + math.Min(255, t.col[i]*k)*t.w
				}
			}
			c.R, c.G, c.B = uint8(p[0]+0.5), uint8(p[1]+0.5), uint8(p[2]+0.5)
			img.SetNRGBA(x, y, c)
		}
	}
}
