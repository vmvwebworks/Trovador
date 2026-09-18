package main

import (
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"sort"
)

// Plantillas de la pletina: la caja de casete y la cinta, fotos con la
// cartulina y la etiqueta en blanco. Se les quita el fondo (un damero
// "de transparencia" pintado, o negro), se abre en transparente la zona
// blanca donde va la carátula y, en la cinta, se recorta un buje para poder
// hacerlo girar por encima. Se imprimen las coordenadas en % para el CSS.
//
//	go run ./tools/cabinet case assets/case.png frontend/deck/case.png
//	go run ./tools/cabinet tape assets/tape.png frontend/deck/tape.png frontend/deck/hub.png

func loadImage(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	// La extensión miente a veces (un JPG llamado .png): se mira el contenido.
	img, _, err := image.Decode(f)
	if err != nil {
		panic(err)
	}
	return img
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}

func savePNG(path string, img image.Image) {
	o, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer o.Close()
	if err := png.Encode(o, img); err != nil {
		panic(err)
	}
}

// El fondo: un damero "de transparencia" pintado (dos grises, distintos en
// cada imagen: se miden en las esquinas), negro o blanco (esquinas lisas). Las
// casillas propagan el relleno; los tonos intermedios (los bordes
// comprimidos entre casillas) se cruzan, pero solo unos pocos seguidos, para
// que el relleno no se cuele por el plástico claro de la caja.
var blackBackground bool // fondo liso: negro (o blanco, con whiteBackground)
var whiteBackground bool
var bgLevels [2]int

func measureBackground(img *image.NRGBA) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	dark, light := 0, 0
	hist := map[int]int{}
	for _, p := range []image.Point{{0, 0}, {W - 1, 0}, {0, H - 1}, {W - 1, H - 1}} {
		if c := img.NRGBAAt(p.X, p.Y); max(c.R, c.G, c.B) < 14 {
			dark++
		} else if c.R >= 240 && c.G >= 240 && c.B >= 240 {
			light++
		}
		for dy := 0; dy < 60; dy++ {
			for dx := 0; dx < 60; dx++ {
				x, y := p.X, p.Y
				if x == 0 {
					x += dx
				} else {
					x -= dx
				}
				if y == 0 {
					y += dy
				} else {
					y -= dy
				}
				c := img.NRGBAAt(x, y)
				mx, mn := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
				if mx-mn <= 10 {
					hist[int(mx)/4*4]++
				}
			}
		}
	}
	blackBackground = dark >= 3 || light >= 3
	whiteBackground = light >= 3
	// Los dos niveles más frecuentes (separados al menos 16).
	best := [2]int{-1, -1}
	for k := 0; k < 2; k++ {
		bv := -1
		for lv, n := range hist {
			if (k == 1 && abs(float64(lv-best[0])) < 16) || (bv >= 0 && n <= hist[bv]) {
				continue
			}
			bv = lv
		}
		best[k] = bv
	}
	if best[1] < 0 {
		best[1] = best[0]
	}
	bgLevels = [2]int{best[0] + 2, best[1] + 2}
}

func isBackground(c color.NRGBA) bool {
	mx, mn := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
	if mx-mn > 10 {
		return false
	}
	if blackBackground {
		if whiteBackground {
			return mx >= 236
		}
		return mx < 14
	}
	for _, lv := range bgLevels {
		if lv >= 0 && abs(float64(int(mx)-lv)) <= 8 {
			return true
		}
	}
	return false
}

func isSoftBackground(c color.NRGBA) bool {
	mx, mn := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	return mx-mn <= 34 && int(mx) >= lo-10 && int(mx) <= hi+10 // el JPEG tiñe un poco los bordes
}

// keyBackground deja transparente el fondo conectado con el borde, sin
// entrar en la zona protegida (la cartulina blanca, que también es blanca).
func keyBackground(img *image.NRGBA, protect image.Rectangle) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	measureBackground(img)
	measurePeriod(img)
	visited := make([]bool, W*H)
	type node struct {
		image.Point
		soft int // píxeles "blandos" seguidos que se llevan recorridos
	}
	// Semillas: los bordes de la imagen y, además, cualquier sitio donde se
	// vea el damero (las dos casillas alrededor): los huecos cerrados, como
	// el que queda entre la bocina y la caja, no se alcanzan desde el borde.
	stack := []node{{image.Pt(0, 0), 0}, {image.Pt(W-1, 0), 0}, {image.Pt(0, H-1), 0}, {image.Pt(W-1, H-1), 0}, {image.Pt(W/2, 0), 0}, {image.Pt(W/2, H-1), 0}, {image.Pt(0, H/2), 0}, {image.Pt(W-1, H/2), 0}}
	if blackBackground {
		// Sobre fondo liso, los huecos cerrados (bajo la bocina) son manchas
		// del color del fondo de 25 px o más, totalmente lisas: una sombra
		// del objeto no llega a tanto.
		for y := 12; y < H-12; y += 6 {
			for x := 12; x < W-12; x += 6 {
				if !image.Pt(x, y).In(protect) && solidBackground(img, x, y, 12) {
					stack = append(stack, node{image.Pt(x, y), 0})
				}
			}
		}
	}
	if !blackBackground {
		for y := 8; y < H-8; y += 6 {
			for x := 8; x < W-8; x += 6 {
				if !image.Pt(x, y).In(protect) && (checkerPixel(img, x, y) || (grayInRange(img, x, y) && periodic(img, x, y) >= 3)) {
					stack = append(stack, node{image.Pt(x, y), 0})
				}
			}
		}
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if p.X < 0 || p.Y < 0 || p.X >= W || p.Y >= H || visited[p.Y*W+p.X] || p.In(protect) {
			continue
		}
		c := img.NRGBAAt(p.X, p.Y)
		// Un píxel del color de una casilla propaga sin límite solo si a su
		// alrededor se ve el damero (las dos casillas): un gris liso del
		// objeto que coincida con una casilla (la sombra de la funda) se
		// come como mucho unos píxeles desde el fondo, como los bordes
		// comprimidos entre casillas (1-2 px), que hay que cruzar pero sin
		// colarse por el plástico claro de la caja.
		soft := p.soft + 1
		if (blackBackground && isBackground(c)) || (!blackBackground && checkerPixel(img, p.X, p.Y)) {
			soft = 0
		} else if !isBackground(c) && !isSoftBackground(c) {
			continue
		}
		if soft > 6 {
			continue
		}
		visited[p.Y*W+p.X] = true
		img.SetNRGBA(p.X, p.Y, color.NRGBA{0, 0, 0, 0})
		for _, q := range []image.Point{{p.X + 1, p.Y}, {p.X - 1, p.Y}, {p.X, p.Y + 1}, {p.X, p.Y - 1}} {
			stack = append(stack, node{q, soft})
		}
	}
	if !blackBackground {
		cleanFringe(img)
	}
	// Bordes: lo que queda pegado al fondo y es claro se atenúa un poco para
	// que no quede un halo blanco.
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			c := img.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			near := img.NRGBAAt(x-1, y).A == 0 || img.NRGBAAt(x+1, y).A == 0 || img.NRGBAAt(x, y-1).A == 0 || img.NRGBAAt(x, y+1).A == 0
			if near && isBackground(c) {
				c.A = 110
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func isWhite(c color.NRGBA) bool {
	mx, mn := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
	return mx > 208 && mx-mn < 18
}

// solidWhite: blanco y con todo el entorno (±rad) blanco: el damero de fondo
// también tiene blancos, pero con cuadros grises a pocos píxeles.
func solidWhite(img *image.NRGBA, x, y, rad int) bool {
	b := img.Bounds()
	lo, hi := uint8(255), uint8(0)
	for dy := -rad; dy <= rad; dy += 4 {
		for dx := -rad; dx <= rad; dx += 4 {
			p := image.Pt(x+dx, y+dy)
			if !p.In(b) {
				return false
			}
			c := img.NRGBAAt(p.X, p.Y)
			if !isWhite(c) {
				return false
			}
			m := max(c.R, c.G, c.B)
			lo, hi = min(lo, m), max(hi, m)
		}
	}
	return hi-lo < 14 // plano: el damero alterna gris y blanco
}

// whiteRect: el rectángulo blanco grande (la cartulina o la etiqueta):
// filas con más de `frac` de píxeles blancos y, dentro, las columnas.
func whiteRect(img *image.NRGBA, frac float64) image.Rectangle {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	rowsOK := make([]bool, H)
	for y := 0; y < H; y++ {
		n := 0
		for x := 0; x < W; x++ {
			if solidWhite(img, x, y, 14) {
				n++
			}
		}
		rowsOK[y] = float64(n) > frac*float64(W)
	}
	// La racha de filas más larga.
	bestS, bestL := 0, 0
	for y := 0; y < H; {
		if !rowsOK[y] {
			y++
			continue
		}
		s := y
		for y < H && rowsOK[y] {
			y++
		}
		if y-s > bestL {
			bestS, bestL = s, y-s
		}
	}
	top, bottom := bestS, bestS+bestL
	colsOK := make([]bool, W)
	for x := 0; x < W; x++ {
		n := 0
		for y := top; y < bottom; y++ {
			if solidWhite(img, x, y, 14) {
				n++
			}
		}
		colsOK[x] = float64(n) > 0.35*float64(bottom-top)
	}
	bestS, bestL = 0, 0
	for x := 0; x < W; {
		if !colsOK[x] {
			x++
			continue
		}
		s := x
		for x < W && colsOK[x] {
			x++
		}
		if x-s > bestL {
			bestS, bestL = s, x-s
		}
	}
	return image.Rect(bestS, top, bestS+bestL, bottom)
}

// openWhite deja transparente lo blanco dentro de r (donde irá la carátula),
// conservando como veladura lo que no es blanco puro (sombras y reflejos
// del plástico). Con un borde suave.
func openWhite(img *image.NRGBA, r image.Rectangle, feather int, skip image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if image.Pt(x, y).In(skip) {
				continue
			}
			c := img.NRGBAAt(x, y)
			l := (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
			// 255 -> transparente; 200 -> ~55 % opaco; más oscuro, opaco.
			keep := ramp(float64(255-l), 0, 90)
			e := float64(min(x-r.Min.X, r.Max.X-1-x, y-r.Min.Y, r.Max.Y-1-y))
			if e < float64(feather) {
				keep = keep + (1-keep)*(1-e/float64(feather))
			}
			c.A = uint8(float64(c.A) * keep)
			img.SetNRGBA(x, y, c)
		}
	}
}

func pctRect(r image.Rectangle, W, H int) string {
	return fmt.Sprintf("left %.2f%% top %.2f%% width %.2f%% height %.2f%%",
		float64(r.Min.X)*100/float64(W), float64(r.Min.Y)*100/float64(H), float64(r.Dx())*100/float64(W), float64(r.Dy())*100/float64(H))
}

// caseTemplate: la caja de pie con la cartulina en blanco.
func caseTemplate(args []string) {
	img := toNRGBA(loadImage(args[0]))
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	card := whiteRect(img, 0.3)
	keyBackground(img, card)
	despeckle(img, 400)
	thinOut(img, 3)
	openWhite(img, card, 6, image.Rectangle{})
	savePNG(args[1], img)
	fmt.Println("caja:", W, "x", H)
	fmt.Println("  cartulina:", pctRect(card, W, H))
}

// tapeTemplate: la cinta con la etiqueta en blanco; además recorta un buje.
func tapeTemplate(args []string) {
	img := toNRGBA(loadImage(args[0]))
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	label := whiteRect(img, 0.12)
	keyBackground(img, label)
	despeckle(img, 400)
	thinOut(img, 3)
	// La ventana: la franja oscura dentro de la etiqueta.
	win := darkRect(img, label)
	openWhite(img, label, 6, win)
	// Los bujes: los dos círculos claros dentro de la ventana.
	lx, ly, lr := hub(img, image.Rect(win.Min.X, win.Min.Y, (win.Min.X+win.Max.X)/2, win.Max.Y))
	// El buje derecho es el espejo del izquierdo respecto al centro de la
	// ventana (por la ventana se ve el fondo y estorba al medirlo).
	rx, ry := float64(win.Min.X+win.Max.X)-lx, ly
	r := int(lr) + 3
	// Por la ventana se veía el damero de fondo: fuera, salvo los bujes y la
	// cinta enrollada; y el agujero del buje, transparente también.
	openWindow(img, win, [][3]float64{{lx, ly, float64(r)}, {rx, ry, float64(r)}})
	hubImg := image.NewNRGBA(image.Rect(0, 0, 2*r, 2*r))
	for y := 0; y < 2*r; y++ {
		for x := 0; x < 2*r; x++ {
			dx, dy := float64(x-r)+0.5, float64(y-r)+0.5
			d := math.Sqrt(dx*dx + dy*dy)
			if d > float64(r) {
				continue
			}
			c := img.NRGBAAt(int(lx)-r+x, int(ly)-r+y)
			if d > float64(r)-2 {
				c.A = uint8(float64(c.A) * (float64(r) - d) / 2)
			}
			hubImg.SetNRGBA(x, y, c)
		}
	}
	savePNG(args[1], img)
	savePNG(args[2], hubImg)
	fmt.Println("cinta:", W, "x", H)
	fmt.Println("  etiqueta:", pctRect(label, W, H))
	fmt.Println("  ventana:", pctRect(win, W, H))
	fmt.Printf("  buje izq: cx %.2f%% cy %.2f%%  buje der: cx %.2f%% cy %.2f%%  diámetro %.2f%% del ancho\n",
		lx*100/float64(W), ly*100/float64(H), rx*100/float64(W), ry*100/float64(H), float64(2*r)*100/float64(W))
}

// darkRect: el rectángulo oscuro (la ventana) dentro de r.
func darkRect(img *image.NRGBA, r image.Rectangle) image.Rectangle {
	dark := func(c color.NRGBA) bool { return max(c.R, c.G, c.B) < 70 }
	rows := []int{}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		n := 0
		for x := r.Min.X; x < r.Max.X; x++ {
			if dark(img.NRGBAAt(x, y)) {
				n++
			}
		}
		if float64(n) > 0.15*float64(r.Dx()) {
			rows = append(rows, y)
		}
	}
	if len(rows) == 0 {
		return image.Rectangle{}
	}
	top, bottom := rows[0], rows[len(rows)-1]+1
	cols := []int{}
	for x := r.Min.X; x < r.Max.X; x++ {
		n := 0
		for y := top; y < bottom; y++ {
			if dark(img.NRGBAAt(x, y)) {
				n++
			}
		}
		if float64(n) > 0.2*float64(bottom-top) {
			cols = append(cols, x)
		}
	}
	if len(cols) == 0 {
		return image.Rectangle{}
	}
	return image.Rect(cols[0], top, cols[len(cols)-1]+1, bottom)
}

// hub: centro y radio del círculo claro dentro de r.
func hub(img *image.NRGBA, r image.Rectangle) (cx, cy, rad float64) {
	var sx, sy, n float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			if max(c.R, c.G, c.B) > 200 {
				sx += float64(x)
				sy += float64(y)
				n++
			}
		}
	}
	if n == 0 {
		return float64(r.Min.X+r.Max.X) / 2, float64(r.Min.Y+r.Max.Y) / 2, float64(r.Dy()) / 3
	}
	cx, cy = sx/n, sy/n
	// Radio: hasta donde llega lo claro (percentil alto de distancias).
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			if max(c.R, c.G, c.B) > 200 {
				d := math.Hypot(float64(x)-cx, float64(y)-cy)
				if d > rad && d < float64(r.Dy())/2 {
					rad = d
				}
			}
		}
	}
	return cx + 0.5, cy + 0.5, rad
}

// openWindow: dentro de la ventana, el damero de fondo (lo que se vería a
// través) se hace transparente, menos los bujes; y en cada buje, el agujero
// central (que también enseñaba el damero) se abre.
func openWindow(img *image.NRGBA, win image.Rectangle, hubs [][3]float64) {
	for y := win.Min.Y; y < win.Max.Y; y++ {
		for x := win.Min.X; x < win.Max.X; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			inHub := false
			for _, h := range hubs {
				d := math.Hypot(px-h[0], py-h[1])
				if d <= h[2] {
					inHub = true
					if d < h[2]*0.62 {
						img.SetNRGBA(x, y, color.NRGBA{}) // el agujero del buje
					}
				}
			}
			if inHub {
				continue
			}
			// Lo que no es la cinta enrollada (oscura) es el damero visto a
			// través: se pinta el interior oscuro de la carcasa.
			c := img.NRGBAAt(x, y)
			if max(c.R, c.G, c.B) > 110 {
				img.SetNRGBA(x, y, color.NRGBA{20, 20, 24, 255})
			}
		}
	}
}

// despeckle quita las motas que quedan sueltas tras quitar el fondo: los
// grupos de píxeles opacos con menos de `minPx` píxeles.
func despeckle(img *image.NRGBA, minPx int) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	seen := make([]bool, W*H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if seen[y*W+x] || img.NRGBAAt(x, y).A == 0 {
				continue
			}
			comp := []image.Point{}
			stack := []image.Point{{x, y}}
			seen[y*W+x] = true
			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				comp = append(comp, p)
				for _, q := range []image.Point{{p.X + 1, p.Y}, {p.X - 1, p.Y}, {p.X, p.Y + 1}, {p.X, p.Y - 1}} {
					if q.X < 0 || q.Y < 0 || q.X >= W || q.Y >= H || seen[q.Y*W+q.X] || img.NRGBAAt(q.X, q.Y).A == 0 {
						continue
					}
					seen[q.Y*W+q.X] = true
					stack = append(stack, q)
				}
			}
			if len(comp) <= minPx {
				for _, p := range comp {
					img.SetNRGBA(p.X, p.Y, color.NRGBA{})
				}
			}
		}
	}
}

// thinOut quita las rebabas: píxeles opacos con transparente a ambos lados
// (a `n` píxeles o menos), en horizontal o en vertical. Unas pasadas bastan
// para comerse una rebaba de hasta 2n de grosor.
func thinOut(img *image.NRGBA, n int) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	clear := func(x, y int) bool { return x < 0 || y < 0 || x >= W || y >= H || img.NRGBAAt(x, y).A == 0 }
	for pass := 0; pass < 3; pass++ {
		var gone []image.Point
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				if img.NRGBAAt(x, y).A == 0 {
					continue
				}
				h, v := true, true
				for d := 1; d <= n; d++ {
					h = h && (clear(x-d, y) || clear(x+d, y))
					v = v && (clear(x, y-d) || clear(x, y+d))
				}
				if h || v {
					gone = append(gone, image.Pt(x, y))
				}
			}
		}
		for _, p := range gone {
			img.SetNRGBA(p.X, p.Y, color.NRGBA{})
		}
	}
}

// checkerAround: alrededor de (x, y) se ve el damero, aunque esté en sombra
// (los generadores pintan una sombra suave alrededor del objeto): entre los
// grises de la ventana, sin contar lo que es claramente objeto (mucho más
// oscuro que las casillas), hay dos grupos separados, cada uno apretado y
// con bastantes píxeles. Un gris liso del objeto solo tiene un grupo.
func checkerAround(img *image.NRGBA, x, y int) bool {
	b := img.Bounds()
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	if hi-lo < 16 {
		return false
	}
	vals := make([]int, 0, 169)
	tot := 0
	for dy := -12; dy <= 12; dy += 2 {
		for dx := -12; dx <= 12; dx += 2 {
			p := image.Pt(x+dx, y+dy)
			if !p.In(b) {
				continue // en el borde de la imagen, la ventana se queda dentro
			}
			tot++
			c := img.NRGBAAt(p.X, p.Y)
			mx, mn := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
			if mx-mn <= 14 && int(mx) >= lo-60 && int(mx) <= hi+10 {
				vals = append(vals, int(mx))
			}
		}
	}
	n := len(vals)
	if n < tot*20/100 {
		return false
	}
	sort.Ints(vals)
	best, gap := -1, 0
	for i := n / 5; i <= n-n/5; i++ {
		if g := vals[i] - vals[i-1]; g > gap {
			best, gap = i, g
		}
	}
	return best > 0 && gap >= (hi-lo)*40/100 && vals[best-1]-vals[0] <= 32 && vals[n-1]-vals[best] <= 32
}

// checkerPixel: el píxel es un gris del rango de las casillas (en sombra
// incluso) y a su alrededor se ve el damero.
func checkerPixel(img *image.NRGBA, x, y int) bool {
	c := img.NRGBAAt(x, y)
	mx, mn := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	return mx-mn <= 14 && int(mx) >= lo-60 && int(mx) <= hi+10 && checkerAround(img, x, y)
}

// bgPeriod: el lado de las casillas, medido en la esquina (la primera racha
// de un mismo nivel por la fila 2).
var bgPeriod int

func measurePeriod(img *image.NRGBA) {
	b := img.Bounds()
	W := b.Dx()
	level := func(x int) int {
		c := img.NRGBAAt(x, 2)
		return int(max(c.R, c.G, c.B)) / 12
	}
	bgPeriod = 0
	l0 := level(0)
	for x := 1; x < W/4; x++ {
		if level(x) != l0 {
			bgPeriod = x
			break
		}
	}
	if bgPeriod < 6 {
		bgPeriod = 20
	}
}

// periodic: cuántas direcciones (0-4) confirman que en (x, y) hay damero:
// a media casilla el nivel cambia (o ya es transparente) y a una casilla
// entera vuelve a ser el mismo (o ya es transparente). Vale también en la
// sombra del objeto, donde las casillas están oscurecidas pero siguen
// alternando. Un metal gris liso no alterna.
func periodic(img *image.NRGBA, x, y int) int {
	b := img.Bounds()
	c := img.NRGBAAt(x, y)
	v := int(max(c.R, c.G, c.B))
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	n := 0
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		h := image.Pt(x+d[0]*bgPeriod/2, y+d[1]*bgPeriod/2)
		f := image.Pt(x+d[0]*bgPeriod, y+d[1]*bgPeriod)
		if !h.In(b) || !f.In(b) {
			continue
		}
		ch, cf := img.NRGBAAt(h.X, h.Y), img.NRGBAAt(f.X, f.Y)
		okH := ch.A == 0 || (abs(float64(int(max(ch.R, ch.G, ch.B))-v)) >= float64(hi-lo)*0.4 && int(max(ch.R, ch.G, ch.B))-int(min(ch.R, ch.G, ch.B)) <= 14)
		okF := cf.A == 0 || (abs(float64(int(max(cf.R, cf.G, cf.B))-v)) <= 16 && int(max(cf.R, cf.G, cf.B))-int(min(cf.R, cf.G, cf.B)) <= 14)
		if okH && okF {
			n++
		}
	}
	return n
}

// cleanFringe quita los restos del damero pegados al objeto (en su sombra,
// donde las casillas están oscurecidas y el relleno no las reconoce):
// píxeles grises del rango de las casillas, a menos de 3 px de algo ya
// transparente, con el damero confirmado en dos direcciones. Varias pasadas.
func cleanFringe(img *image.NRGBA) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	nearClear := func(x, y int) bool {
		for d := 1; d <= 3; d++ {
			for _, p := range []image.Point{{x + d, y}, {x - d, y}, {x, y + d}, {x, y - d}} {
				if p.In(b) && img.NRGBAAt(p.X, p.Y).A == 0 {
					return true
				}
			}
		}
		return false
	}
	for pass := 0; pass < 12; pass++ {
		var gone []image.Point
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				c := img.NRGBAAt(x, y)
				if c.A == 0 {
					continue
				}
				mx, mn := int(max(c.R, c.G, c.B)), int(min(c.R, c.G, c.B))
				if mx-mn > 14 || mx < lo-70 || mx > hi+10 || !nearClear(x, y) {
					continue
				}
				if fringeCell(img, x, y) {
					gone = append(gone, image.Pt(x, y))
				}
			}
		}
		if len(gone) == 0 {
			break
		}
		for _, p := range gone {
			img.SetNRGBA(p.X, p.Y, color.NRGBA{})
		}
	}
}

// grayInRange: gris (poca saturación) del rango de las casillas, sombra
// incluida.
func grayInRange(img *image.NRGBA, x, y int) bool {
	c := img.NRGBAAt(x, y)
	mx, mn := int(max(c.R, c.G, c.B)), int(min(c.R, c.G, c.B))
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	return c.A > 0 && mx-mn <= 14 && mx >= lo-70 && mx <= hi+10
}

// fringeCell: el píxel es un resto de casilla pegado al objeto: por el lado
// que da a lo transparente no hay nada, y a lo largo del borde, a media
// casilla, el nivel alterna (la casilla vecina, del otro nivel, o ya
// quitada) y a una casilla vuelve a ser el mismo. El borde liso del
// plástico de la caja no alterna.
func fringeCell(img *image.NRGBA, x, y int) bool {
	b := img.Bounds()
	c := img.NRGBAAt(x, y)
	v := int(max(c.R, c.G, c.B))
	lo, hi := min(bgLevels[0], bgLevels[1]), max(bgLevels[0], bgLevels[1])
	gray := func(q color.NRGBA) bool { return int(max(q.R, q.G, q.B))-int(min(q.R, q.G, q.B)) <= 14 }
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		clear := false
		for k := 1; k <= 3 && !clear; k++ {
			p := image.Pt(x+d[0]*k, y+d[1]*k)
			clear = p.In(b) && img.NRGBAAt(p.X, p.Y).A == 0
		}
		if !clear {
			continue
		}
		ok := true
		for _, s := range []int{1, -1} {
			h := image.Pt(x+d[1]*s*bgPeriod/2, y+d[0]*s*bgPeriod/2)
			f := image.Pt(x+d[1]*s*bgPeriod, y+d[0]*s*bgPeriod)
			if h.In(b) {
				if q := img.NRGBAAt(h.X, h.Y); q.A > 0 && !(gray(q) && abs(float64(int(max(q.R, q.G, q.B))-v)) >= float64(hi-lo)*0.4) {
					ok = false
				}
			}
			if f.In(b) {
				if q := img.NRGBAAt(f.X, f.Y); q.A > 0 && !(gray(q) && abs(float64(int(max(q.R, q.G, q.B))-v)) <= 16) {
					ok = false
				}
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// solidBackground: todo el cuadrado ±rad alrededor es del color del fondo
// liso (negro o blanco).
func solidBackground(img *image.NRGBA, x, y, rad int) bool {
	for dy := -rad; dy <= rad; dy += 2 {
		for dx := -rad; dx <= rad; dx += 2 {
			if !isBackground(img.NRGBAAt(x+dx, y+dy)) {
				return false
			}
		}
	}
	return true
}
