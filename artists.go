package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
)

func sortNatural(names []string) {
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
}

// Iconos de las carpetas de artista: el logo del grupo si lo hay, si no su
// foto, y si no se encuentra nada, un mosaico con las carátulas de sus
// discos. Para no colgarle a un grupo la foto de otro que se llama igual
// ("Frost" hay veinte), el artista se identifica siempre a través de uno de
// sus discos: el MBID que dejan las etiquetas, o una búsqueda del disco en
// la fuente. Nunca se busca por el nombre a secas.

const kindArtist = "artist"

// Imágenes que el usuario (o Kodi, o nosotros) deja en la carpeta del
// artista; la primera que exista manda y no se pregunta a nadie. Los
// "logo.*" van delante porque en la carpeta se prefiere el logo del grupo
// a una foto suya.
var artistImageNames = []string{"logo.png", "logo.jpg", "logo.gif", "logo.webp", "folder.jpg", "folder.png", "artist.jpg", "artist.png"}

// artistPic es la imagen encontrada para un artista.
type artistPic struct {
	data   []byte
	logo   bool   // logo del grupo en vez de foto
	ext    string // con qué extensión se guarda (.png, .jpg, .gif, .webp)
	source string // de dónde salió
}

// file: con qué nombre se guarda en la carpeta del artista. El prefijo es
// lo que luego decide en localArtistImage si es logo o foto, y la extensión
// tiene que ser una de artistImageNames para volver a encontrarla.
func (p artistPic) file() string {
	ext := p.ext
	switch ext {
	case ".png", ".jpg", ".gif", ".webp":
	default:
		ext = ".jpg"
	}
	if p.logo {
		return "logo" + ext
	}
	return "folder" + ext
}

// imgExt: la extensión con la que guardar una imagen de una URL. Solo los
// formatos que sabemos decodificar; los logos de Metal Archives son tan
// pronto JPG como PNG o GIF.
func imgExt(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	switch ext := strings.ToLower(path.Ext(u)); ext {
	case ".png", ".gif", ".webp":
		return ext
	case ".jpg", ".jpeg":
		return ".jpg"
	}
	return ""
}

// artistAlbums: subcarpetas con audio (los discos del artista).
func artistAlbums(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "_") && !strings.HasPrefix(e.Name(), ".") {
			if p := filepath.Join(dir, e.Name()); countAudio(p) > 0 {
				out = append(out, p)
			}
		}
	}
	sortNatural(out)
	return out
}

// localArtistImage: la imagen propia de la carpeta, si la hay.
func localArtistImage(dir string) (artistPic, bool) {
	for _, name := range artistImageNames {
		p := filepath.Join(dir, name)
		if data, err := os.ReadFile(p); err == nil && len(data) > 100 {
			return artistPic{data: data, logo: strings.HasPrefix(name, "logo."), ext: imgExt(name), source: name}, true
		}
	}
	return artistPic{}, false
}

// artistMBID busca el MBID del artista: en el análisis de esta sesión, en
// las etiquetas de sus discos y, si no, buscando uno de sus discos en
// MusicBrainz (la edición que cuadra en título y artista da el MBID).
func (a *App) artistMBID(ctx context.Context, name string, albums []string) string {
	for _, alb := range albums {
		if an := a.batch.get(alb); an != nil && an.detail != nil && an.detail.Release.FirstArtistID != "" {
			return an.detail.Release.FirstArtistID
		}
	}
	for i, alb := range albums {
		if i >= 3 {
			break
		}
		if card, err := a.AlbumCard(alb); err == nil && card.MBID != "" {
			return card.MBID
		}
	}
	for i, alb := range albums {
		if i >= 2 || ctx.Err() != nil {
			break
		}
		card, err := a.AlbumCard(alb)
		if err != nil || card.Album == "" {
			continue
		}
		rels, err := musicbrainz.searchReleases(ctx, name, stripParen(card.Album), false)
		if err != nil {
			continue
		}
		for _, r := range rels {
			if r.FirstArtistID != "" && similarity(r.Title, card.Album) >= 0.8 && similarity(r.FirstArtist, name) >= 0.8 {
				return r.FirstArtistID
			}
		}
	}
	return ""
}

// findArtistPic recorre las fuentes. Primero los logos, que es lo que se
// quiere ver en la carpeta: TheAudioDB por MBID y Metal Archives por
// nombre. Solo si no aparece ninguno se baja una foto (TheAudioDB, Discogs,
// Deezer o Bandcamp, identificando al artista por uno de sus discos).
//
// Con logosOnly no se buscan fotos: la carpeta ya tiene imagen y solo
// estamos mirando si hay un logo con el que mejorarla.
func (a *App) findArtistPic(ctx context.Context, name string, albums []string, logosOnly bool) (artistPic, error) {
	var thumb string // la foto de TheAudioDB, por si no hay ningún logo
	if mbid := a.artistMBID(ctx, name, albums); mbid != "" {
		if logo, t, err := audioDBArtist(ctx, mbid); err == nil {
			thumb = t
			if data, err := fetchBytes(ctx, logo); err == nil {
				return artistPic{data: data, logo: true, ext: imgExt(logo), source: "TheAudioDB (logo)"}, nil
			}
		}
	}
	if u, err := metalArchivesLogo(ctx, name); err == nil {
		if data, err := maGet(ctx, u); err == nil && len(data) > 100 {
			return artistPic{data: data, logo: true, ext: imgExt(u), source: "Metal Archives (logo)"}, nil
		}
	} else if err == errMetalArchivesDown {
		a.maDownOnce()
	}
	if logosOnly {
		return artistPic{}, fmt.Errorf("sin logo en las fuentes")
	}
	if data, err := fetchBytes(ctx, thumb); err == nil {
		return artistPic{data: data, ext: imgExt(thumb), source: "TheAudioDB (foto)"}, nil
	}
	for i, alb := range albums {
		if i >= 2 || ctx.Err() != nil {
			break
		}
		card, err := a.AlbumCard(alb)
		if err != nil || card.Album == "" {
			continue
		}
		album := stripParen(card.Album)
		if token := a.searchOpts(false).DiscogsToken; token != "" {
			if u, err := discogsArtistPic(ctx, name, album, token); err == nil {
				if data, err := fetchBytes(ctx, u); err == nil {
					return artistPic{data: data, ext: imgExt(u), source: "Discogs"}, nil
				}
			}
		}
		if u, err := deezerArtistPic(ctx, name, album); err == nil {
			if data, err := fetchBytes(ctx, u); err == nil {
				return artistPic{data: data, ext: imgExt(u), source: "Deezer"}, nil
			}
		}
		if u, err := bandcampArtistPic(ctx, name, album); err == nil {
			if data, err := fetchBytes(ctx, u); err == nil {
				return artistPic{data: data, ext: imgExt(u), source: "Bandcamp"}, nil
			}
		}
	}
	return artistPic{}, fmt.Errorf("sin imagen en las fuentes")
}

// ---- Metal Archives: el logo de la banda -----------------------------------

// Metal Archives es la enciclopedia del metal, y tiene el logo de casi
// cualquier grupo: justo los que no están ni en MusicBrainz ni en
// TheAudioDB, que es donde se queda corta la búsqueda por MBID.
//
// Peticiones de una en una y espaciadas un segundo, como con MusicBrainz. Y
// si no contesta —hay quien lo tiene bloqueado por su operador, y el sitio
// se cae a ratos— se deja de intentar en toda la sesión: si no, cada
// carpeta sin logo se comería el tiempo de espera.
var (
	maBase  = "https://www.metal-archives.com" // lo cambian los tests
	maDelay = time.Second                      // espera entre peticiones
)

var metalArchives = struct {
	mu     sync.Mutex
	last   time.Time
	fails  int  // fallos seguidos: con maMaxFails se deja de intentar
	down   bool // ha fallado alguna vez en esta sesión
	warned bool // ya se ha anotado en el registro
	client *http.Client
}{client: &http.Client{Timeout: 8 * time.Second}}

// maUnreachable: en esta sesión Metal Archives no ha llegado a contestar.
// Los artistas que se queden sin logo por eso se marcan como provisionales
// (ver artistSig) para volver a intentarlo cuando el sitio vuelva.
func maUnreachable() bool {
	metalArchives.mu.Lock()
	defer metalArchives.mu.Unlock()
	return metalArchives.down
}

// maDownOnce lo anota en el registro una sola vez por sesión: con 200
// carpetas, repetirlo en cada una no informa de nada.
func (a *App) maDownOnce() {
	metalArchives.mu.Lock()
	warned := metalArchives.warned
	metalArchives.warned = true
	metalArchives.mu.Unlock()
	if !warned {
		a.libLog("aviso: Metal Archives no contesta (lo bloquean algunos operadores, y el sitio se cae a ratos): esta vez, sin sus logos. Las carpetas afectadas se vuelven a intentar en el próximo lote")
	}
}

// maMaxFails: fallos de conexión seguidos tras los que se da por inalcanzable.
const maMaxFails = 2

var errMetalArchivesDown = fmt.Errorf("Metal Archives no responde")

func maGet(ctx context.Context, u string) ([]byte, error) {
	metalArchives.mu.Lock()
	if metalArchives.fails >= maMaxFails {
		metalArchives.mu.Unlock()
		return nil, errMetalArchivesDown
	}
	if wait := maDelay - time.Since(metalArchives.last); wait > 0 {
		time.Sleep(wait)
	}
	metalArchives.last = time.Now()
	metalArchives.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+userAgent)
	resp, err := metalArchives.client.Do(req)
	if err != nil {
		if ctx.Err() == nil { // cancelar el lote no cuenta como fallo suyo
			metalArchives.mu.Lock()
			metalArchives.fails++
			metalArchives.down = true
			metalArchives.mu.Unlock()
		}
		return nil, err
	}
	defer resp.Body.Close()
	metalArchives.mu.Lock()
	metalArchives.fails = 0 // contesta: un 404 no es estar caído
	metalArchives.mu.Unlock()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

var (
	// La búsqueda devuelve cada resultado como el <a> de la banda.
	maBandRe = regexp.MustCompile(`href="([^"]+/bands/[^"]+)"[^>]*>([^<]+)<`)
	// En la ficha, el enlace del logo a tamaño completo.
	maLogoRe = regexp.MustCompile(`id="logo"[^>]*href="([^"]+)"`)
)

// metalArchivesLogo: la URL del logo de la banda. Se exige que el nombre
// coincida exactamente (nada de parecidos: "Frost" son veinte bandas).
func metalArchivesLogo(ctx context.Context, name string) (string, error) {
	data, err := maGet(ctx, maBase+"/search/ajax-band-search/?field=name&query="+url.QueryEscape(name))
	if err != nil {
		return "", err
	}
	var raw struct {
		AaData [][]string `json:"aaData"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	band, ok := maBandURL(raw.AaData, name)
	if !ok {
		return "", fmt.Errorf("no está en Metal Archives")
	}
	if !strings.HasPrefix(band, maBase+"/") { // solo se siguen enlaces del propio sitio
		return "", fmt.Errorf("enlace de banda inesperado: %s", band)
	}
	page, err := maGet(ctx, band)
	if err != nil {
		return "", err
	}
	logo := maLogoURL(page)
	if logo == "" {
		return "", fmt.Errorf("su ficha no tiene logo")
	}
	return logo, nil
}

// maBandURL: la ficha de la banda que se llama exactamente así. Cada fila
// de la búsqueda trae el <a> del grupo y, a veces, sus alias detrás.
func maBandURL(rows [][]string, name string) (string, bool) {
	want := normalizeName(name)
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		m := maBandRe.FindStringSubmatch(row[0])
		if m != nil && normalizeName(html.UnescapeString(m[2])) == want {
			return html.UnescapeString(m[1]), true
		}
	}
	return "", false
}

// maLogoURL: el enlace al logo a tamaño completo de una ficha de banda.
func maLogoURL(page []byte) string {
	m := maLogoRe.FindSubmatch(page)
	if m == nil {
		return ""
	}
	return html.UnescapeString(string(m[1]))
}

// audioDBArtist: logo y foto de TheAudioDB por MBID (clave pública de
// pruebas; sin ella no hay búsqueda por nombre, que tampoco queremos).
func audioDBArtist(ctx context.Context, mbid string) (logo, thumb string, err error) {
	var raw struct {
		Artists []struct {
			Logo  string `json:"strArtistLogo"`
			Thumb string `json:"strArtistThumb"`
		} `json:"artists"`
	}
	if err := getJSON(ctx, http.MethodGet, "https://www.theaudiodb.com/api/v1/json/2/artist-mb.php?i="+url.QueryEscape(mbid), nil, nil, &raw); err != nil {
		return "", "", err
	}
	if len(raw.Artists) == 0 {
		return "", "", fmt.Errorf("no está en TheAudioDB")
	}
	return raw.Artists[0].Logo, raw.Artists[0].Thumb, nil
}

// deezerArtistPic: busca el disco; si Deezer lo tiene de ese artista, la
// foto es la de su ficha de artista.
func deezerArtistPic(ctx context.Context, artist, album string) (string, error) {
	var raw struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name      string `json:"name"`
				PictureXL string `json:"picture_xl"`
			} `json:"artist"`
		} `json:"data"`
	}
	q := url.QueryEscape(strings.TrimSpace(artist + " " + album))
	if err := getJSON(ctx, http.MethodGet, "https://api.deezer.com/search/album?limit=5&q="+q, nil, nil, &raw); err != nil {
		return "", err
	}
	for _, d := range raw.Data {
		if similarity(d.Title, album) >= 0.8 && similarity(d.Artist.Name, artist) >= 0.8 && d.Artist.PictureXL != "" &&
			!strings.Contains(d.Artist.PictureXL, "/artist//") { // sin foto: Deezer devuelve una URL vacía por dentro
			return d.Artist.PictureXL, nil
		}
	}
	return "", fmt.Errorf("no está en Deezer")
}

var bandPhotoRe = regexp.MustCompile(`<img[^>]+class="band-photo"[^>]+src="([^"]+)"`)

// bandcampArtistPic: busca el disco; la página del grupo lleva su foto.
func bandcampArtistPic(ctx context.Context, artist, album string) (string, error) {
	rels, err := searchBandcamp(ctx, artist, album)
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if similarity(r.Title, album) < 0.8 || similarity(r.Artist, artist) < 0.8 {
			continue
		}
		_, albumURL := parseKey(r.ID)
		u, err := url.Parse(albumURL)
		if err != nil {
			continue
		}
		page, err := fetchBytes(ctx, u.Scheme+"://"+u.Host+"/")
		if err != nil {
			continue
		}
		if m := bandPhotoRe.FindSubmatch(page); m != nil {
			return bigBandcampImage(string(m[1])), nil
		}
	}
	return "", fmt.Errorf("no está en Bandcamp")
}

// fetchBytes descarga una URL (imagen o página) con un tope de tamaño.
func fetchBytes(ctx context.Context, u string) ([]byte, error) {
	if strings.TrimSpace(u) == "" {
		return nil, fmt.Errorf("sin URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+userAgent)
	resp, err := webClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil || len(data) < 100 {
		return nil, fmt.Errorf("respuesta vacía")
	}
	return data, nil
}

// ---- dibujo ----------------------------------------------------------------

// renderArtistIcon: logo sobre fondo (oscuro o claro según el logo), foto
// en círculo, o abanico de carátulas si no hay imagen del artista.
func renderArtistIcon(pic image.Image, logo bool, covers []image.Image) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	switch {
	case pic != nil && logo:
		drawLogo(dst, pic)
	case pic != nil:
		drawPhoto(dst, pic)
	default:
		drawCovers(dst, covers)
	}
	return dst
}

// Los tres dibujos de artista ocupan exactamente la misma caja: la misma
// placa cuadrada redondeada, con la misma sombra y el mismo filo, lleve
// foto, logo o carátulas. Mezclar círculos y cuadrados de distinto tamaño
// hacía bailar la rejilla del Explorador.
const (
	plateX, plateY = 16.0, 16.0
	plateSide      = 224.0
	plateR         = 18.0
)

// artistPlate pinta la sombra de la placa y devuelve la figura para seguir.
func artistPlate(dst *image.RGBA) *shape {
	s := newShape()
	s.roundRect(plateX+4, plateY+8, plateSide, plateSide, plateR)
	s.fill(dst, rgba(0, 0, 0, 90))
	return s
}

// plateFill rellena la placa entera (color, degradado o imagen situada).
func plateFill(s *shape, dst *image.RGBA, src image.Image) {
	s.roundRect(plateX, plateY, plateSide, plateSide, plateR)
	s.fill(dst, src)
}

// plateEdge remata la placa con un filo de dos píxeles.
func plateEdge(s *shape, dst *image.RGBA, c image.Image) {
	s.roundRect(plateX, plateY, plateSide, plateSide, plateR)
	s.roundRectDir(plateX+2, plateY+2, plateSide-4, plateSide-4, plateR-2, true)
	s.fill(dst, c)
}

// drawLogo: el logo contenido (sin recortar) en la placa.
func drawLogo(dst *image.RGBA, logo image.Image) {
	s := artistPlate(dst)
	bg, edge, invert := logoPlate(logo)
	if invert {
		logo = invertMono(logo)
	}
	plateFill(s, dst, bg)
	plateEdge(s, dst, edge)
	fitContain(dst, logo, image.Rect(32, 32, 224, 224))
}

// logoPlate elige el fondo de la placa y su filo. Un logo con transparencia
// va siempre sobre negro, que es como se ven los logos (y como se leen a
// tamaño de icono); si el logo es oscuro y monocromo se invierte a blanco,
// como en una camiseta negra. Uno opaco —los de Metal Archives son
// imágenes con su propio fondo— traería su rectángulo pegado en medio de la
// placa: para que no se vea la junta, la placa se pinta del color de los
// bordes del propio logo.
func logoPlate(logo image.Image) (bg, edge *image.Uniform, invert bool) {
	if c, ok := borderColor(logo); ok {
		if (0.299*float64(c.R)+0.587*float64(c.G)+0.114*float64(c.B))/255 > 0.55 {
			return image.NewUniform(c), rgba(0, 0, 0, 40), false
		}
		return image.NewUniform(c), rgba(255, 255, 255, 40), false
	}
	dark := luminance(logo) < 0.45
	return rgba(0, 0, 0, 255), rgba(255, 255, 255, 45), dark && isMono(logo)
}

// isMono: el logo es de un solo tono (negro, blanco, grises) y se puede
// invertir sin cambiarle el color.
func isMono(img image.Image) bool {
	b := img.Bounds()
	var sat, n float64
	for i := 0; i < 48; i++ {
		for j := 0; j < 48; j++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+b.Dx()*i/48, b.Min.Y+b.Dy()*j/48)).(color.NRGBA)
			if c.A < 64 {
				continue
			}
			hi, lo := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
			sat += float64(hi - lo)
			n++
		}
	}
	return n == 0 || sat/n < 24
}

// invertMono devuelve el logo con los colores invertidos (negro -> blanco)
// y la misma transparencia.
func invertMono(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			out.SetNRGBA(x, y, color.NRGBA{255 - c.R, 255 - c.G, 255 - c.B, c.A})
		}
	}
	return out
}

// borderColor: el color del marco de la imagen, si es opaca y de un color
// parejo (el fondo de un logo en JPG). ok=false si tiene transparencia por
// ahí —entonces no hay rectángulo que disimular— o si el borde es de muchos
// colores, que ya no es un fondo liso y pintarlo quedaría peor.
func borderColor(img image.Image) (color.NRGBA, bool) {
	b := img.Bounds()
	var px []color.NRGBA
	at := func(x, y int) bool {
		c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
		if c.A < 250 {
			return false
		}
		px = append(px, c)
		return true
	}
	for i := 0; i <= 32; i++ {
		x := b.Min.X + (b.Dx()-1)*i/32
		y := b.Min.Y + (b.Dy()-1)*i/32
		if !at(x, b.Min.Y) || !at(x, b.Max.Y-1) || !at(b.Min.X, y) || !at(b.Max.X-1, y) {
			return color.NRGBA{}, false
		}
	}
	var r, g, bl int
	for _, c := range px {
		r, g, bl = r+int(c.R), g+int(c.G), bl+int(c.B)
	}
	n := len(px)
	mean := color.NRGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255}
	for _, c := range px {
		if abs(int(c.R)-int(mean.R)) > 40 || abs(int(c.G)-int(mean.G)) > 40 || abs(int(c.B)-int(mean.B)) > 40 {
			return color.NRGBA{}, false
		}
	}
	return mean, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// drawPhoto: la foto recortada a la placa, con el mismo filo y brillo.
func drawPhoto(dst *image.RGBA, photo image.Image) {
	s := artistPlate(dst)
	plateFill(s, dst, fitCover(photo, plateX, plateY, plateSide, plateSide))
	plateEdge(s, dst, rgba(255, 255, 255, 110))
	plateFill(s, dst, gloss(90, 70, 200, 35))
}

// drawCovers: sin imagen del artista, sus carátulas en mosaico dentro de la
// misma placa (hasta tres); sin ninguna, la nota gris.
func drawCovers(dst *image.RGBA, covers []image.Image) {
	if len(covers) == 0 {
		covers = []image.Image{placeholder()}
	}
	if len(covers) > 3 {
		covers = covers[:3]
	}
	// El mosaico se compone aparte y luego se vierte por la placa, así
	// hereda sus esquinas redondeadas.
	const gap = 3
	x0, y0, side := int(plateX), int(plateY), int(plateSide)
	half := (side - gap) / 2
	var boxes []image.Rectangle
	switch len(covers) {
	case 1:
		boxes = []image.Rectangle{image.Rect(x0, y0, x0+side, y0+side)}
	case 2:
		boxes = []image.Rectangle{
			image.Rect(x0, y0, x0+half, y0+side),
			image.Rect(x0+half+gap, y0, x0+side, y0+side)}
	default: // una grande a la izquierda y dos apiladas a la derecha
		boxes = []image.Rectangle{
			image.Rect(x0, y0, x0+half, y0+side),
			image.Rect(x0+half+gap, y0, x0+side, y0+half),
			image.Rect(x0+half+gap, y0+half+gap, x0+side, y0+side)}
	}
	mosaic := image.NewRGBA(image.Rect(x0, y0, x0+side, y0+side))
	draw.Draw(mosaic, mosaic.Bounds(), rgba(18, 18, 20, 255), image.Point{}, draw.Src)
	for i, c := range covers {
		b := boxes[i]
		draw.Draw(mosaic, b, fitCover(c, b.Min.X, b.Min.Y, b.Dx(), b.Dy()), b.Min, draw.Src)
	}
	s := artistPlate(dst)
	plateFill(s, dst, mosaic)
	plateEdge(s, dst, rgba(255, 255, 255, 40))
	plateFill(s, dst, gloss(90, 70, 200, 35))
}

// fitContain escala la imagen entera dentro del rectángulo (sin recortar),
// centrada, respetando su transparencia.
func fitContain(dst *image.RGBA, src image.Image, r image.Rectangle) {
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	scale := math.Min(float64(r.Dx())/sw, float64(r.Dy())/sh)
	w, h := int(sw*scale), int(sh*scale)
	x := r.Min.X + (r.Dx()-w)/2
	y := r.Min.Y + (r.Dy()-h)/2
	xdraw.CatmullRom.Scale(dst, image.Rect(x, y, x+w, y+h), src, sb, draw.Over, nil)
}

// luminance: claridad media (0..1) de los píxeles opacos, para decidir el
// fondo de un logo.
func luminance(img image.Image) float64 {
	b := img.Bounds()
	var sum, n float64
	for i := 0; i < 48; i++ {
		for j := 0; j < 48; j++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+b.Dx()*i/48, b.Min.Y+b.Dy()*j/48)).(color.NRGBA)
			if c.A < 64 {
				continue
			}
			sum += (0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)) / 255
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return sum / n
}

// ---- icono de la carpeta de artista ----------------------------------------

// writeArtistIcon deja el icono en la carpeta del artista. Busca la imagen
// (propia, o en las fuentes, y la guarda en la carpeta como logo.png o
// folder.jpg para no volver a pedirla) y, si no hay, usa las carátulas.
func (a *App) writeArtistIcon(ctx context.Context, dir string) error {
	name := filepath.Base(dir)
	albums := artistAlbums(dir)
	pic, ok := localArtistImage(dir)
	// Se prefiere el logo: si la carpeta solo tiene una foto, se mira si hay
	// logo con el que sustituirla (la foto se queda donde está, así que
	// borrar el logo.* devuelve la carpeta a como estaba).
	if !ok || !pic.logo {
		if found, err := a.findArtistPic(ctx, name, albums, ok); err == nil {
			if err := os.WriteFile(filepath.Join(dir, found.file()), found.data, 0o644); err != nil {
				return err
			}
			a.libLog("%s: imagen de artista de %s", name, found.source)
			pic, ok = found, true
		}
	}
	var img image.Image
	if ok {
		if decoded, _, err := image.Decode(bytes.NewReader(pic.data)); err == nil {
			img = decoded
		} else {
			a.libLog("aviso: %s: no pude leer %s (%v)", name, pic.source, err)
			ok = false
		}
	}
	var covers []image.Image
	if !ok {
		for _, alb := range albums {
			if len(covers) >= 3 {
				break
			}
			if c, err := a.coverImage(alb); err == nil && c != nil {
				covers = append(covers, c)
			}
		}
	}
	ico, err := encodeICO(renderArtistIcon(img, ok && pic.logo, covers))
	if err != nil {
		return err
	}
	prev := readIni(dir)
	if err := writeHidden(filepath.Join(dir, iconFileName), ico, attrHidden); err != nil {
		return err
	}
	tip := name
	if n := len(albums); n == 1 {
		tip += " · 1 disco"
	} else if n > 1 {
		tip += fmt.Sprintf(" · %d discos", n)
	}
	if err := writeIni(dir, prev, kindArtist, true, "", tip); err != nil {
		return err
	}
	if err := setAttrs(dir, attrReadonly, 0); err != nil {
		return err
	}
	shellNotify(dir)
	return nil
}

// artistIconAuto: como autoIcon, para una carpeta de artista.
func (a *App) artistIconAuto(dir string) {
	a.mu.Lock()
	on := a.settings.FolderIcons
	a.mu.Unlock()
	if !on {
		return
	}
	if err := a.writeArtistIcon(a.ctx, dir); err != nil {
		a.libLog("aviso: icono de artista %s: %v", filepath.Base(dir), err)
	}
}

// artistIconVersion: la versión del dibujo de artista, aparte de la de los
// discos. Al cambiarla, el lote rehace solo las carpetas de artista.
const artistIconVersion = "3"

// artistSig: firma de una carpeta de artista (imagen propia y discos).
//
// Cuando la carpeta se queda sin logo porque Metal Archives no contestaba,
// la firma lleva además "|sin-ma": así queda provisional. En el siguiente
// lote, mientras el sitio siga sin contestar la firma vuelve a calcularse
// igual y la carpeta se salta sin gastar peticiones; y el primer lote en el
// que el sitio responda la calculará sin la marca, no cuadrará con la
// guardada, y la carpeta se rehará para ir a por su logo.
func artistSig(dir string) string {
	var b strings.Builder
	b.WriteString(artistIconVersion + "|" + kindArtist)
	pic := ""
	for _, name := range artistImageNames {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			fmt.Fprintf(&b, "|%s:%d", name, info.Size())
			pic = name
			break
		}
	}
	for _, alb := range artistAlbums(dir) {
		b.WriteString("|" + filepath.Base(alb))
	}
	if !strings.HasPrefix(pic, "logo.") && maUnreachable() {
		b.WriteString("|sin-ma")
	}
	return b.String()
}

// artistDirs: las carpetas que contienen discos pero no son un disco (las
// de artista), sin la raíz de la colección.
func artistDirs(root string, albums []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, alb := range albums {
		parent := filepath.Dir(alb)
		if seen[parent] || strings.EqualFold(parent, root) || countAudio(parent) > 0 {
			continue
		}
		seen[parent] = true
		out = append(out, parent)
	}
	sortNatural(out)
	return out
}

// refreshArtistIcon rehace el icono de artista si algo cambió (imagen
// propia, discos, versión del dibujo); si no, lo salta.
func (a *App) refreshArtistIcon(ctx context.Context, dir string) (skipped bool, err error) {
	prev := readIni(dir)
	if prev.kind == kindArtist && prev.sig == artistSig(dir) && fileExists(filepath.Join(dir, iconFileName)) {
		return true, nil
	}
	return false, a.writeArtistIcon(ctx, dir)
}

// discogsArtistPic: busca el disco en Discogs (con token), coge su primer
// artista acreditado y la imagen principal de la ficha del artista. Discogs
// tiene foto de casi cualquier grupo con una demo publicada.
func discogsArtistPic(ctx context.Context, artist, album, token string) (string, error) {
	rels, err := searchDiscogs(ctx, artist, album, token)
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if similarity(r.Title, album) < 0.8 || similarity(firstArtist(r.Artist), artist) < 0.8 {
			continue
		}
		_, id := parseKey(r.ID)
		var rel struct {
			Artists []struct {
				ID int64 `json:"id"`
			} `json:"artists"`
		}
		if err := getJSON(ctx, http.MethodGet, "https://api.discogs.com/releases/"+id+"?token="+url.QueryEscape(token), nil, nil, &rel); err != nil || len(rel.Artists) == 0 {
			continue
		}
		var art struct {
			Images []struct {
				URI  string `json:"uri"`
				Type string `json:"type"`
			} `json:"images"`
		}
		u := fmt.Sprintf("https://api.discogs.com/artists/%d?token=%s", rel.Artists[0].ID, url.QueryEscape(token))
		if err := getJSON(ctx, http.MethodGet, u, nil, nil, &art); err != nil {
			continue
		}
		for _, img := range art.Images {
			if img.Type == "primary" && img.URI != "" {
				return img.URI, nil
			}
		}
		if len(art.Images) > 0 && art.Images[0].URI != "" {
			return art.Images[0].URI, nil
		}
	}
	return "", fmt.Errorf("no está en Discogs")
}
