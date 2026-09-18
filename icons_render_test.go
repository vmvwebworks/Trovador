package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestRenderIcons dibuja los cuatro tipos con una carátula sintética y los
// guarda en ICONS_OUT (si está definida) para mirarlos a ojo.
func TestRenderIcons(t *testing.T) {
	var cover image.Image
	synth := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			synth.Set(x, y, color.RGBA{uint8(200 - y/2), uint8(60 + x/3), uint8(90 + (x+y)/4), 255})
		}
	}
	for y := 120; y < 180; y++ {
		for x := 40; x < 260; x++ {
			synth.Set(x, y, color.RGBA{250, 240, 200, 255})
		}
	}
	cover = synth
	if p := os.Getenv("ICONS_COVER"); p != "" { // una carátula real para mirar el resultado
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if img, _, err := image.Decode(f); err == nil {
			cover = img
		}
	}
	out := os.Getenv("ICONS_OUT")
	for _, kind := range []string{kindCD, kindVinyl, kindCassette, kindDigital} {
		img := renderIcon(kind, cover)
		if img.Bounds().Dx() != iconSize {
			t.Fatalf("%s: tamaño %v", kind, img.Bounds())
		}
		// Algo tiene que haberse pintado y el fondo debe seguir transparente.
		if img.RGBAAt(128, 128).A == 0 || img.RGBAAt(1, 1).A != 0 {
			t.Errorf("%s: centro alpha=%d, esquina alpha=%d", kind, img.RGBAAt(128, 128).A, img.RGBAAt(1, 1).A)
		}
		ico, err := encodeICO(img)
		if err != nil {
			t.Fatal(err)
		}
		if len(ico) < 1000 || ico[2] != 1 || ico[4] != 6 {
			t.Errorf("%s: ico raro (%d bytes, cabecera %v)", kind, len(ico), ico[:6])
		}
		if out != "" {
			f, _ := os.Create(filepath.Join(out, kind+".png"))
			_ = png.Encode(f, img)
			f.Close()
			_ = os.WriteFile(filepath.Join(out, kind+".ico"), ico, 0o644)
		}
	}
	if renderIcon(kindCD, nil) == nil {
		t.Error("sin carátula debe dibujar igualmente")
	}
}

func TestMediaKind(t *testing.T) {
	cases := map[string]string{
		"CD": kindCD, "CD+CD": kindCD, "Enhanced CD": kindCD, "HDCD": kindCD, "CDr": kindCD,
		"Vinyl": kindVinyl, "12\" Vinyl": kindVinyl, "7\" Vinyl": kindVinyl, "Vinyl, LP, Album": kindVinyl,
		"Cassette": kindCassette, "Cass, Album": kindCassette, "MC": kindCassette,
		"Digital Media": kindDigital, "File, MP3": kindDigital, "Digital": kindDigital,
		"": "", "DVD": "",
	}
	for in, want := range cases {
		if got := mediaKind(in); got != want {
			t.Errorf("mediaKind(%q) = %q, esperaba %q", in, got, want)
		}
	}
}

// TestRenderArtistIcons: logo (claro y oscuro), foto y mosaico de carátulas.
func TestRenderArtistIcons(t *testing.T) {
	out := os.Getenv("ICONS_OUT")
	save := func(name string, img *image.RGBA) {
		if out == "" {
			return
		}
		f, _ := os.Create(filepath.Join(out, name+".png"))
		_ = png.Encode(f, img)
		f.Close()
	}
	// Un "logo": texto no hay, así que unas barras claras sobre transparente.
	logo := image.NewRGBA(image.Rect(0, 0, 400, 140))
	for y := 30; y < 110; y++ {
		for x := 20; x < 380; x++ {
			if (x/40)%2 == 0 || y > 90 {
				logo.Set(x, y, color.RGBA{240, 240, 240, 255})
			}
		}
	}
	light := renderArtistIcon(logo, true, nil)
	if light.RGBAAt(30, 30).R > 20 {
		t.Error("logo con transparencia: placa negra")
	}
	save("artist-logo-light", light)
	dark := image.NewRGBA(logo.Bounds())
	for y := 30; y < 110; y++ {
		for x := 20; x < 380; x++ {
			if logo.RGBAAt(x, y).A > 0 {
				dark.Set(x, y, color.RGBA{20, 20, 20, 255})
			}
		}
	}
	// Oscuro y monocromo: placa negra igualmente, y el logo invertido a blanco
	// para que se vea (en el centro de una barra, el píxel debe ser claro).
	if img := renderArtistIcon(dark, true, nil); img.RGBAAt(30, 30).R > 20 || img.RGBAAt(80, 142).R < 200 {
		t.Errorf("logo oscuro: placa %v, logo %v", img.RGBAAt(30, 30), img.RGBAAt(80, 142))
	} else {
		save("artist-logo-dark", img)
	}
	photo := image.NewRGBA(image.Rect(0, 0, 300, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 300; x++ {
			photo.Set(x, y, color.RGBA{uint8(120 + x/3), uint8(80 + y/4), uint8(60), 255})
		}
	}
	p := renderArtistIcon(photo, false, nil)
	if p.RGBAAt(128, 128).A == 0 || p.RGBAAt(4, 4).A != 0 {
		t.Error("foto en la placa: centro opaco, esquina transparente")
	}
	save("artist-photo", p)
	var covers []image.Image
	for i := 0; i < 3; i++ {
		c := image.NewRGBA(image.Rect(0, 0, 200, 200))
		for y := 0; y < 200; y++ {
			for x := 0; x < 200; x++ {
				c.Set(x, y, color.RGBA{uint8(60 + 60*i + x/4), uint8(200 - 50*i - y/4), uint8(90 + 40*i), 255})
			}
		}
		covers = append(covers, c)
	}
	fan := renderArtistIcon(nil, false, covers)
	if fan.RGBAAt(128, 140).A == 0 {
		t.Error("mosaico vacío")
	}
	save("artist-fan", fan)
	save("artist-fan1", renderArtistIcon(nil, false, covers[:1]))
	save("artist-none", renderArtistIcon(nil, false, nil))
	if _, err := encodeICO(fan); err != nil {
		t.Fatal(err)
	}

	// Logo opaco (los de Metal Archives son imágenes con su propio fondo):
	// la placa tiene que tomar ese fondo, o se vería el rectángulo pegado.
	opaque := image.NewRGBA(image.Rect(0, 0, 400, 140))
	bg := color.RGBA{18, 22, 30, 255}
	draw.Draw(opaque, opaque.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	for y := 40; y < 100; y++ {
		for x := 30; x < 370; x++ {
			if (x/30)%2 == 0 {
				opaque.Set(x, y, color.RGBA{235, 235, 240, 255})
			}
		}
	}
	img := renderArtistIcon(opaque, true, nil)
	if c := img.RGBAAt(24, 128); c.R != bg.R || c.G != bg.G || c.B != bg.B {
		t.Errorf("la placa de un logo opaco debe ir de su color de fondo %v, no %v", bg, c)
	}
	save("artist-logo-opaque", img)

	// Y si el borde no es liso (una foto metida como logo), se deja la placa
	// de siempre en vez de pintarla de un color inventado.
	if _, ok := borderColor(photo); ok {
		t.Error("el borde de una foto con degradado no es un fondo liso")
	}
}
