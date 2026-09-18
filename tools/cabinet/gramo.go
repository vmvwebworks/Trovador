package main

import (
	"fmt"
	"image"
	"image/color"
	"math"

	xdraw "golang.org/x/image/draw"
)

// Plantillas del gramófono: la máquina (fieltro verde en el plato para
// medir la elipse donde va el disco; el brazo descansa fuera del plato,
// así que el disco no lo tapa), el disco (etiqueta blanca abierta para la
// carátula) y la funda (cartón blanco abierto). Todas con el damero de
// fondo quitado; se imprimen las coordenadas en % para el CSS.
//
//	go run ./tools/cabinet gramo assets/gramophone.png frontend/gramo/gramophone.png [sin]
//	go run ./tools/cabinet record assets/record.png frontend/gramo/record.png
//	go run ./tools/cabinet sleeve assets/sleeve.png frontend/gramo/sleeve.png

// isGreen: el fieltro del plato (verde saturado).
func isGreen(c color.NRGBA) bool {
	h, s, v := rgbToHSV(c.R, c.G, c.B)
	return h > 80 && h < 160 && s > 0.3 && v > 0.18
}

func gramoTemplate(args []string) {
	img := toNRGBA(loadImage(args[0]))
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	keyBackground(img, image.Rectangle{})
	keepLargest(img)
	if !blackBackground {
		// Sobre damero pintado, el contorno hay que adivinarlo: limpieza de
		// restos y pelado de la rebaba. Sobre negro no hace falta (y pelaría
		// el níquel del codo).
		despeckle(img, 3000)
		thinOut(img, 3)
		lightSpecks(img, 600)
		grayEdge(img, 3)
	}
	// El plato: el grupo de píxeles verdes más grande (el latón tiene algún
	// reflejo verdoso suelto). Centro y semiejes, de su caja: los extremos
	// izquierdo, derecho y de arriba/abajo del fieltro no los tapa el brazo.
	seen := make([]bool, W*H)
	var bestBox image.Rectangle
	bestN := 0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if seen[y*W+x] || img.NRGBAAt(x, y).A == 0 || !isGreen(img.NRGBAAt(x, y)) {
				continue
			}
			box := image.Rect(x, y, x+1, y+1)
			cnt := 0
			stack := []image.Point{{x, y}}
			seen[y*W+x] = true
			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				cnt++
				box = box.Union(image.Rect(p.X, p.Y, p.X+1, p.Y+1))
				for _, q := range []image.Point{{p.X + 1, p.Y}, {p.X - 1, p.Y}, {p.X, p.Y + 1}, {p.X, p.Y - 1}} {
					if q.X < 0 || q.Y < 0 || q.X >= W || q.Y >= H || seen[q.Y*W+q.X] {
						continue
					}
					if c := img.NRGBAAt(q.X, q.Y); c.A == 0 || !isGreen(c) {
						continue
					}
					seen[q.Y*W+q.X] = true
					stack = append(stack, q)
				}
			}
			if cnt > bestN {
				bestN, bestBox = cnt, box
			}
		}
	}
	if bestN == 0 {
		panic("no encuentro el fieltro verde del plato")
	}
	cx, cy := float64(bestBox.Min.X+bestBox.Max.X)/2, float64(bestBox.Min.Y+bestBox.Max.Y)/2
	a, bb := float64(bestBox.Dx())/2, float64(bestBox.Dy())/2
	// Etalonaje a la luz de la interfaz (el baño de acento a lo bruto, como
	// en la gramola, deja el latón y la caoba manchados). "sin" lo evita.
	if len(args) < 3 || args[2] != "sin" {
		grade(img, 1)
	}
	savePNG(args[1], img)
	fmt.Println("gramófono:", W, "x", H)
	fmt.Printf("  plato: cx %.2f%% cy %.2f%% a %.2f%% del ancho, b %.2f%% del alto (b/a en px %.3f)\n",
		cx*100/float64(W), cy*100/float64(H), a*100/float64(W), bb*100/float64(H), bb/a)
}

// recordTemplate: el disco visto desde arriba; la etiqueta blanca, abierta.
func recordTemplate(args []string) {
	img := toNRGBA(loadImage(args[0]))
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	keyBackground(img, image.Rectangle{})
	despeckle(img, 400)
	// El disco: caja de lo oscuro (el vinilo), que los restos claros del
	// damero pegados al borde no cuentan.
	minX, minY, maxX, maxY := W, H, 0, 0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if c := img.NRGBAAt(x, y); c.A > 0 && max(c.R, c.G, c.B) < 90 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	cx, cy := float64(minX+maxX+1)/2, float64(minY+maxY+1)/2
	r := float64(max(maxX-minX, maxY-minY)+1) / 2
	// El disco es un círculo: fuera de él no hay nada que conservar (así
	// se van los restos del damero y el halo del borde).
	clipCircle(img, cx, cy, r+1)
	// La etiqueta: el círculo claro del centro (radio: hasta donde llega el
	// blanco desde el centro, mirando por radios).
	lr := 0.0
	for k := 0; k < 360; k += 5 {
		ang := float64(k) * math.Pi / 180
		d := 0.0
		for d < r {
			c := img.NRGBAAt(int(cx+d*math.Cos(ang)), int(cy+d*math.Sin(ang)))
			if !isWhite(c) && d > r*0.05 {
				break
			}
			d++
		}
		lr += d
	}
	lr /= 72
	// Abierta en transparente (el blanco), con el agujero y las sombras como
	// veladura; borde suave.
	for y := int(cy - lr); y <= int(cy+lr); y++ {
		for x := int(cx - lr); x <= int(cx+lr); x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			if d > lr {
				continue
			}
			c := img.NRGBAAt(x, y)
			l := (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
			keep := ramp(float64(255-l), 0, 90)
			if e := lr - d; e < 3 {
				keep = keep + (1-keep)*(1-e/3)
			}
			c.A = uint8(float64(c.A) * keep)
			img.SetNRGBA(x, y, c)
		}
	}
	// Recortado al disco.
	out := image.NewNRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			out.SetNRGBA(x-minX, y-minY, img.NRGBAAt(x, y))
		}
	}
	grade(out, 0.7) // el vinilo, a la luz de la interfaz
	savePNG(args[1], out)
	fmt.Println("disco:", out.Bounds().Dx(), "x", out.Bounds().Dy())
	fmt.Printf("  etiqueta: diámetro %.2f%% del disco\n", 2*lr*100/float64(maxX-minX+1))
}

// sleeveTemplate: la funda de frente; el cartón blanco, abierto. La funda es
// un cuadrado (el cartón, con un pelo de borde) y el disco que asoma por la
// derecha: la forma es geométrica, así que se recorta por ella (con los
// colores originales: sobre negro, el vinilo negro no se puede distinguir
// del fondo, pero su borde brillante sí dice hasta dónde llega).
func sleeveTemplate(args []string) {
	img := toNRGBA(loadImage(args[0]))
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	card := whiteRect(img, 0.5)
	measureBackground(img)
	// El disco: círculo centrado en la altura del cartón; su borde derecho,
	// lo más a la derecha que hay algo que no es fondo (sobre damero, lo
	// oscuro; sobre negro, lo que no es negro).
	cy := float64(card.Min.Y+card.Max.Y) / 2
	right := card.Max.X
	for y := card.Min.Y; y < card.Max.Y; y++ {
		for x := card.Max.X; x < W; x++ {
			c := img.NRGBAAt(x, y)
			mx := int(max(c.R, c.G, c.B))
			if (blackBackground && mx > 22 && !whiteBackground) || (!blackBackground && mx < 90 && !isBackground(c) && !isSoftBackground(c)) {
				right = max(right, x)
			}
		}
	}
	r := float64(card.Dy()) / 2
	rcx := float64(right) - r
	edge := float64(card.Dx()) * 0.012
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			inCard := float64(x) >= float64(card.Min.X)-edge && float64(x) < float64(card.Max.X)+edge && float64(y) >= float64(card.Min.Y)-edge && float64(y) < float64(card.Max.Y)+edge
			d := math.Hypot(float64(x)+0.5-rcx, float64(y)+0.5-cy)
			c := img.NRGBAAt(x, y)
			switch {
			case inCard || d <= r-1:
				c.A = 255
			case d <= r:
				c.A = uint8(255 * (r - d))
			default:
				c = color.NRGBA{}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	openWhite(img, card, 6, image.Rectangle{})
	// Recortada a lo opaco.
	minX, minY, maxX, maxY := W, H, 0, 0
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if img.NRGBAAt(x, y).A > 0 || image.Pt(x, y).In(card) {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, maxX-minX+1, maxY-minY+1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			out.SetNRGBA(x-minX, y-minY, img.NRGBAAt(x, y))
		}
	}
	grade(out, 0.5) // la funda, un poco (el cartón blanco no ha de verse verde)
	savePNG(args[1], out)
	ow, oh := out.Bounds().Dx(), out.Bounds().Dy()
	fmt.Println("funda:", ow, "x", oh)
	fmt.Println("  cartón:", pctRect(card.Sub(image.Pt(minX, minY)), ow, oh))
}

// keepLargest deja solo el grupo de píxeles opacos más grande: la máquina es
// una sola pieza y lo demás son restos del fondo.
func keepLargest(img *image.NRGBA) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	label := make([]int32, W*H)
	var sizes []int
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if label[y*W+x] != 0 || img.NRGBAAt(x, y).A == 0 {
				continue
			}
			id := int32(len(sizes) + 1)
			n := 0
			stack := []image.Point{{x, y}}
			label[y*W+x] = id
			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				n++
				for _, q := range []image.Point{{p.X + 1, p.Y}, {p.X - 1, p.Y}, {p.X, p.Y + 1}, {p.X, p.Y - 1}} {
					if q.X < 0 || q.Y < 0 || q.X >= W || q.Y >= H || label[q.Y*W+q.X] != 0 || img.NRGBAAt(q.X, q.Y).A == 0 {
						continue
					}
					label[q.Y*W+q.X] = id
					stack = append(stack, q)
				}
			}
			sizes = append(sizes, n)
		}
	}
	best := 0
	for i, n := range sizes {
		if n > sizes[best] {
			best = i
		}
	}
	for i, l := range label {
		if l != 0 && l != int32(best+1) {
			img.SetNRGBA(i%W, i/W, color.NRGBA{})
		}
	}
}

// clipCircle deja transparente lo que queda fuera del círculo (borde suave).
func clipCircle(img *image.NRGBA, cx, cy, r float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			if d > r {
				img.SetNRGBA(x, y, color.NRGBA{})
			} else if d > r-1.5 {
				c := img.NRGBAAt(x, y)
				c.A = uint8(float64(c.A) * (r - d) / 1.5)
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// lightSpecks quita los grupos pequeños de píxeles claros y grises (restos
// de casillas blancas) que tocan algo transparente: bajo la bocina, entre el
// tubo y el codo, queda alguna media casilla cercada de latón.
func lightSpecks(img *image.NRGBA, maxPx int) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	light := func(x, y int) bool {
		c := img.NRGBAAt(x, y)
		mx, mn := int(max(c.R, c.G, c.B)), int(min(c.R, c.G, c.B))
		return c.A > 0 && mx-mn <= 12 && mx >= 190
	}
	seen := make([]bool, W*H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if seen[y*W+x] || !light(x, y) {
				continue
			}
			comp := []image.Point{}
			touches := false
			stack := []image.Point{{x, y}}
			seen[y*W+x] = true
			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				comp = append(comp, p)
				for _, q := range []image.Point{{p.X + 1, p.Y}, {p.X - 1, p.Y}, {p.X, p.Y + 1}, {p.X, p.Y - 1}} {
					if !q.In(b) {
						continue
					}
					if img.NRGBAAt(q.X, q.Y).A == 0 {
						touches = true
						continue
					}
					if seen[q.Y*W+q.X] || !light(q.X, q.Y) {
						continue
					}
					seen[q.Y*W+q.X] = true
					stack = append(stack, q)
				}
			}
			if touches && len(comp) <= maxPx {
				for _, p := range comp {
					img.SetNRGBA(p.X, p.Y, color.NRGBA{})
				}
			}
		}
	}
}

// grayEdge pela el borde: los píxeles grises claros (sin color) pegados a
// lo transparente son restos de las casillas mezclados con el contorno, que
// dejan una rebaba blanca alrededor del latón y la caoba. Se quitan capa a
// capa, `depth` veces; el níquel del codo pierde como mucho esos píxeles.
func grayEdge(img *image.NRGBA, depth int) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	for pass := 0; pass < depth; pass++ {
		var gone []image.Point
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				c := img.NRGBAAt(x, y)
				if c.A == 0 {
					continue
				}
				mx, mn := int(max(c.R, c.G, c.B)), int(min(c.R, c.G, c.B))
				// Gris claro; y en las dos primeras capas, cualquier píxel claro (el
				// contorno mezclado con una casilla blanca lleva algo de color).
				grayish := mx-mn <= 16 && mx >= 110
				light := pass < 2 && mx >= 165 && mx-mn <= 60
				if !grayish && !light {
					continue
				}
				near := false
				for _, q := range []image.Point{{x + 1, y}, {x - 1, y}, {x, y + 1}, {x, y - 1}} {
					if q.In(b) && img.NRGBAAt(q.X, q.Y).A == 0 {
						near = true
						break
					}
				}
				if near {
					gone = append(gone, image.Pt(x, y))
				}
			}
		}
		for _, p := range gone {
			img.SetNRGBA(p.X, p.Y, color.NRGBA{})
		}
	}
}

// logoTemplate: el logo (cuadrado redondeado sobre blanco): las esquinas
// blancas, transparentes; el grande va a build/appicon.png (el icono del
// exe, que Wails saca de ahí) y una versión pequeña a la cabecera.
//
//	go run ./tools/cabinet logo assets/logo.png build/appicon.png frontend/logo.png
func logoTemplate(args []string) {
	img := toNRGBA(loadImage(args[0]))
	keyBackground(img, image.Rectangle{})
	savePNG(args[1], img)
	small := image.NewNRGBA(image.Rect(0, 0, 96, 96))
	xdraw.CatmullRom.Scale(small, small.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	savePNG(args[2], small)
	fmt.Println("logo:", img.Bounds().Dx(), "x", img.Bounds().Dy(), "-> icono y 96 px")
}
