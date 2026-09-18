package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Respuestas reales de Metal Archives —la búsqueda de "Burialkult" y el
// trozo de su ficha donde está el logo— para que el parseo no se rompa a
// ciegas si cambian el sitio.
func TestMetalArchivesParsing(t *testing.T) {
	const search = `{
	"error": "",
	"iTotalRecords": 1,
	"iTotalDisplayRecords": 1,
	"sEcho": 0,
	"aaData": [
				[
			"<a href=\"https://www.metal-archives.com/bands/Burialkult/3540344950\">Burialkult</a> (<strong>a.k.a.</strong> BurialCult, BurialKult) <!-- 12.637093 -->" ,
			"Black Metal" ,
			"Canada"     		]
				]
}`
	var raw struct {
		AaData [][]string `json:"aaData"`
	}
	if err := json.Unmarshal([]byte(search), &raw); err != nil {
		t.Fatal(err)
	}
	const want = "https://www.metal-archives.com/bands/Burialkult/3540344950"
	if got, ok := maBandURL(raw.AaData, "Burialkult"); !ok || got != want {
		t.Errorf("ficha de la banda = %q (ok=%v)", got, ok)
	}
	// Tiene que ser el mismo grupo, no uno que se le parezca.
	if _, ok := maBandURL(raw.AaData, "Burial"); ok {
		t.Error("«Burial» no es «Burialkult»: no debería valer")
	}
	// Mayúsculas y acentos sí dan igual (normalizeName).
	if _, ok := maBandURL(raw.AaData, "BURIALKULT"); !ok {
		t.Error("el nombre debería casar sin mirar mayúsculas")
	}
	if _, ok := maBandURL(nil, "Burialkult"); ok {
		t.Error("sin resultados no hay ficha")
	}

	page := []byte(`<div class="band_name_img">
<a class="image" id="logo" title="Burialkult" href="https://www.metal-archives.com/images/3/5/4/0/3540344950_logo.jpg?3503"><img src="https://www.metal-archives.com/images/3/5/4/0/3540344950_logo.jpg?3503" title="Click to zoom" alt="Burialkult - Logo" border="0" /></a>
</div>
<div class="band_img">
<a class="image" id="photo" title="Burialkult" href="https://www.metal-archives.com/images/3/5/4/0/3540344950_photo.jpg?1241"></a>
</div>`)
	const wantLogo = "https://www.metal-archives.com/images/3/5/4/0/3540344950_logo.jpg?3503"
	if got := maLogoURL(page); got != wantLogo {
		t.Errorf("logo = %q", got) // ojo: no vale coger el _photo
	}
	if got := maLogoURL([]byte(`<div class="band_img"><a id="photo" href="x.jpg"></a></div>`)); got != "" {
		t.Errorf("una ficha sin logo debe dar vacío, no %q", got)
	}
}

// La imagen se guarda con el nombre que decide si es logo o foto, y con la
// extensión que venga de la URL.
func TestArtistPicFile(t *testing.T) {
	cases := []struct {
		url  string
		logo bool
		want string
	}{
		{"https://www.metal-archives.com/images/3/5/4/0/3540344950_logo.jpg?3503", true, "logo.jpg"},
		{"https://www.theaudiodb.com/images/media/artist/logo/xyz.png", true, "logo.png"},
		// Metal Archives también sirve logos en GIF (Angest Herre) y hay que
		// guardarlos como tales: renombrarlos a .jpg los deja sin decodificar.
		{"https://www.metal-archives.com/images/3/5/4/0/3540276562_logo.gif", true, "logo.gif"},
		{"https://api.deezer.com/artist/1/image", false, "folder.jpg"},
		{"https://f4.bcbits.com/img/0012345678_10.JPEG", false, "folder.jpg"},
	}
	for _, c := range cases {
		got := artistPic{logo: c.logo, ext: imgExt(c.url)}.file()
		if got != c.want {
			t.Errorf("%s -> %q, esperaba %q", c.url, got, c.want)
		}
	}
}

// El camino completo (búsqueda -> ficha -> logo) contra un servidor local,
// que es lo más cerca del sitio real que se puede probar sin internet.
func TestMetalArchivesLogo(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			fmt.Fprintf(w, `{"aaData":[["<a href=\"%s/bands/Burialkult/3540344950\">Burialkult</a> (<strong>a.k.a.</strong> BurialCult)","Black Metal","Canada"]]}`, maBase)
		case strings.HasPrefix(r.URL.Path, "/bands/"):
			fmt.Fprintf(w, `<div class="band_name_img"><a class="image" id="logo" title="Burialkult" href="%s/images/3540344950_logo.jpg?3503"><img src="x"/></a></div>`, maBase)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer restoreMA(maBase, maDelay)()
	maBase, maDelay = srv.URL, 0

	got, err := metalArchivesLogo(context.Background(), "Burialkult")
	if err != nil || got != srv.URL+"/images/3540344950_logo.jpg?3503" {
		t.Fatalf("logo = %q, err = %v", got, err)
	}
	if hits != 2 {
		t.Errorf("deberían ser 2 peticiones (búsqueda y ficha), fueron %d", hits)
	}
	if _, err := metalArchivesLogo(context.Background(), "Otra Banda"); err == nil {
		t.Error("un nombre que no está no debería devolver logo")
	}
}

// Si el sitio no contesta (bloqueado por el operador, o caído), se deja de
// intentar: si no, cada carpeta sin logo se comería el tiempo de espera.
func TestMetalArchivesSeRinde(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close() // nadie escucha ya ahí: las peticiones fallan al momento
	defer restoreMA(maBase, maDelay)()
	maBase, maDelay = dead, 0

	for i := 0; i < maMaxFails; i++ {
		if _, err := metalArchivesLogo(context.Background(), "Burialkult"); err == nil {
			t.Fatal("debería fallar: no hay nadie escuchando")
		}
	}
	if _, err := metalArchivesLogo(context.Background(), "Burialkult"); err != errMetalArchivesDown {
		t.Errorf("tras %d fallos debería rendirse, y devolvió %v", maMaxFails, err)
	}
}

// restoreMA deja las variables (y el contador de fallos) como estaban: son
// globales y los tests van en el mismo proceso.
func restoreMA(base string, delay time.Duration) func() {
	return func() {
		maBase, maDelay = base, delay
		metalArchives.mu.Lock()
		metalArchives.fails, metalArchives.down, metalArchives.warned = 0, false, false
		metalArchives.mu.Unlock()
	}
}

// Si el artista se queda sin logo porque el sitio no contestaba, su firma
// queda marcada para volver a intentarlo cuando vuelva.
func TestArtistSigProvisional(t *testing.T) {
	defer restoreMA(maBase, maDelay)()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "folder.jpg"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}

	normal := artistSig(dir)
	if strings.Contains(normal, "sin-ma") {
		t.Fatalf("con el sitio respondiendo la firma no lleva marca: %s", normal)
	}

	metalArchives.mu.Lock()
	metalArchives.down = true
	metalArchives.mu.Unlock()

	caido := artistSig(dir)
	if !strings.HasSuffix(caido, "|sin-ma") {
		t.Errorf("sin logo y con el sitio caído la firma debe quedar provisional: %s", caido)
	}
	// La carpeta se salta mientras siga caído (la firma guardada cuadra)...
	if artistSig(dir) != caido {
		t.Error("con el sitio caído la firma tiene que ser estable, o se rehace en cada lote")
	}
	// ...y en cuanto vuelva, la firma cambia y la carpeta se rehace.
	metalArchives.mu.Lock()
	metalArchives.down = false
	metalArchives.mu.Unlock()
	if artistSig(dir) != normal {
		t.Error("al volver el sitio la firma debe ser la normal, para que no cuadre con la guardada")
	}

	// Con logo ya en la carpeta no hay nada que reintentar.
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	metalArchives.mu.Lock()
	metalArchives.down = true
	metalArchives.mu.Unlock()
	if strings.Contains(artistSig(dir), "sin-ma") {
		t.Error("si ya tiene logo, la firma no debe quedar provisional")
	}
}
