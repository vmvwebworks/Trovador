package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Scrobbling a Last.fm de lo que suena en el reproductor de la app.
//
// Escribir en el perfil de alguien exige firmar cada petición (api_sig: MD5
// de los parámetros ordenados + el shared secret) y una clave de sesión, que
// se consigue una sola vez: auth.getToken → el usuario lo autoriza en
// last.fm desde el navegador → auth.getSession. La sesión no caduca.
//
// Qué cuenta como escucha lo decide la ventana con las reglas de Last.fm
// (pista de más de 30 s, escuchada la mitad o 4 minutos); aquí se envía.
// Lo que no se puede enviar (sin conexión, Last.fm caído) se guarda en
// scrobbles.json y sale con el siguiente o al arrancar.

// scrobbleTrack es una escucha.
type scrobbleTrack struct {
	Artist    string  `json:"artist"`
	Track     string  `json:"track"`
	Album     string  `json:"album"`
	Duration  float64 `json:"duration"`  // segundos
	Timestamp int64   `json:"timestamp"` // cuándo empezó (Unix, UTC)
}

// lastfmState es lo que enseña Ajustes.
type lastfmState struct {
	HasKey    bool   `json:"hasKey"`
	HasSecret bool   `json:"hasSecret"`
	Connected bool   `json:"connected"`
	User      string `json:"user"`
	Enabled   bool   `json:"enabled"` // scrobbling activado
	Pending   int    `json:"pending"` // escuchas guardadas sin enviar
	Error     string `json:"error"`   // el último fallo al enviar
}

var scrobbler = struct {
	mu      sync.Mutex
	token   string // de auth.getToken, mientras se espera la autorización
	queue   []scrobbleTrack
	loaded  bool
	sending sync.Mutex // un envío a la vez
	lastErr string
}{}

// ---- Métodos expuestos a la ventana --------------------------------------

func (a *App) LastfmState() lastfmState {
	a.mu.Lock()
	s := a.settings
	a.mu.Unlock()
	scrobbler.mu.Lock()
	defer scrobbler.mu.Unlock()
	loadQueue()
	return lastfmState{
		HasKey: s.LastfmKey != "", HasSecret: s.LastfmSecret != "",
		Connected: s.LastfmSession != "", User: s.LastfmUser, Enabled: s.Scrobble,
		Pending: len(scrobbler.queue), Error: scrobbler.lastErr,
	}
}

// SaveLastfmSecret guarda el shared secret (hace falta para conectar).
func (a *App) SaveLastfmSecret(secret string) (lastfmState, error) {
	a.mu.Lock()
	a.settings.LastfmSecret = strings.TrimSpace(secret)
	s := a.settings
	a.mu.Unlock()
	return a.LastfmState(), s.save()
}

// LastfmConnect pide un token y abre en el navegador la página de last.fm
// donde el usuario da permiso. La ventana llama luego a LastfmFinish.
func (a *App) LastfmConnect() error {
	key, secret, _ := a.lastfmCreds()
	if key == "" || secret == "" {
		return fmt.Errorf("faltan la API key y el shared secret de Last.fm (Ajustes)")
	}
	var raw struct {
		Token string `json:"token"`
	}
	if err := lastfmCall(http.MethodGet, url.Values{"method": {"auth.getToken"}, "api_key": {key}}, secret, &raw); err != nil {
		return err
	}
	scrobbler.mu.Lock()
	scrobbler.token = raw.Token
	scrobbler.mu.Unlock()
	runtime.BrowserOpenURL(a.ctx, "https://www.last.fm/api/auth/?api_key="+url.QueryEscape(key)+"&token="+url.QueryEscape(raw.Token))
	return nil
}

// LastfmFinish canjea el token autorizado por la sesión. Mientras el
// usuario no haya dado permiso, Last.fm contesta con error 14 y la ventana
// vuelve a probar al rato.
func (a *App) LastfmFinish() (lastfmState, error) {
	key, secret, _ := a.lastfmCreds()
	scrobbler.mu.Lock()
	token := scrobbler.token
	scrobbler.mu.Unlock()
	if token == "" {
		return a.LastfmState(), fmt.Errorf("primero pulsa «conectar»")
	}
	var raw struct {
		Session struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"session"`
	}
	if err := lastfmCall(http.MethodGet, url.Values{"method": {"auth.getSession"}, "api_key": {key}, "token": {token}}, secret, &raw); err != nil {
		return a.LastfmState(), err
	}
	scrobbler.mu.Lock()
	scrobbler.token, scrobbler.lastErr = "", ""
	scrobbler.mu.Unlock()
	a.mu.Lock()
	a.settings.LastfmSession, a.settings.LastfmUser, a.settings.Scrobble = raw.Session.Key, raw.Session.Name, true
	s := a.settings
	a.mu.Unlock()
	if err := s.save(); err != nil {
		return a.LastfmState(), err
	}
	a.libLog("Last.fm: conectado como %s", raw.Session.Name)
	go a.flushScrobbles()
	return a.LastfmState(), nil
}

func (a *App) LastfmDisconnect() (lastfmState, error) {
	a.mu.Lock()
	a.settings.LastfmSession, a.settings.LastfmUser = "", ""
	s := a.settings
	a.mu.Unlock()
	return a.LastfmState(), s.save()
}

func (a *App) SetScrobbling(on bool) (lastfmState, error) {
	a.mu.Lock()
	a.settings.Scrobble = on
	s := a.settings
	a.mu.Unlock()
	if on {
		go a.flushScrobbles()
	}
	return a.LastfmState(), s.save()
}

// NowPlaying avisa a Last.fm de lo que empieza a sonar. Si falla da igual:
// no se reintenta, solo es el «escuchando ahora» del perfil.
func (a *App) NowPlaying(t scrobbleTrack) error {
	key, secret, sk := a.lastfmCreds()
	if !a.scrobbling() || t.Artist == "" || t.Track == "" {
		return nil
	}
	p := url.Values{"method": {"track.updateNowPlaying"}, "api_key": {key}, "sk": {sk}, "artist": {t.Artist}, "track": {t.Track}}
	if t.Album != "" {
		p.Set("album", t.Album)
	}
	if t.Duration > 0 {
		p.Set("duration", strconv.Itoa(int(t.Duration)))
	}
	var raw json.RawMessage
	return lastfmCall(http.MethodPost, p, secret, &raw)
}

// Scrobble apunta una escucha y la envía (con las que hubiera pendientes).
// Aunque no haya Last.fm, cuenta para el gusto de «para ti» (taste.go).
func (a *App) Scrobble(t scrobbleTrack) error {
	countPlay(t.Artist)
	if !a.scrobbling() || t.Artist == "" || t.Track == "" {
		return nil
	}
	if t.Timestamp == 0 {
		t.Timestamp = time.Now().Unix()
	}
	scrobbler.mu.Lock()
	loadQueue()
	scrobbler.queue = append(scrobbler.queue, t)
	saveQueue()
	scrobbler.mu.Unlock()
	flushLater(a)
	return nil
}

// flushLater envía la cola en segundo plano (los tests lo anulan).
var flushLater = func(a *App) { go a.flushScrobbles() }

// ---- envío ------------------------------------------------------------------

func (a *App) lastfmCreds() (key, secret, session string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.settings.LastfmKey), strings.TrimSpace(a.settings.LastfmSecret), a.settings.LastfmSession
}

func (a *App) scrobbling() bool {
	key, secret, sk := a.lastfmCreds()
	a.mu.Lock()
	on := a.settings.Scrobble
	a.mu.Unlock()
	return on && key != "" && secret != "" && sk != ""
}

// flushScrobbles envía la cola de 50 en 50 (lo máximo que admite
// track.scrobble). Al fallar, lo que no salió se queda para la próxima.
func (a *App) flushScrobbles() {
	if !a.scrobbling() {
		return
	}
	scrobbler.sending.Lock()
	defer scrobbler.sending.Unlock()
	key, secret, sk := a.lastfmCreds()
	for {
		scrobbler.mu.Lock()
		loadQueue()
		batch := append([]scrobbleTrack(nil), scrobbler.queue[:min(50, len(scrobbler.queue))]...)
		scrobbler.mu.Unlock()
		if len(batch) == 0 {
			break
		}
		p := url.Values{"method": {"track.scrobble"}, "api_key": {key}, "sk": {sk}}
		for i, t := range batch {
			n := "[" + strconv.Itoa(i) + "]"
			p.Set("artist"+n, t.Artist)
			p.Set("track"+n, t.Track)
			p.Set("timestamp"+n, strconv.FormatInt(t.Timestamp, 10))
			if t.Album != "" {
				p.Set("album"+n, t.Album)
			}
			if t.Duration > 0 {
				p.Set("duration"+n, strconv.Itoa(int(t.Duration)))
			}
		}
		var raw struct {
			Scrobbles struct {
				Attr struct {
					Accepted int `json:"accepted"`
					Ignored  int `json:"ignored"`
				} `json:"@attr"`
			} `json:"scrobbles"`
		}
		err := lastfmCall(http.MethodPost, p, secret, &raw)
		scrobbler.mu.Lock()
		if err != nil {
			scrobbler.lastErr = err.Error()
			scrobbler.mu.Unlock()
			a.libLog("Last.fm: no he podido enviar %d escucha(s), se reintentará: %v", len(batch), err)
			a.emit("lastfm")
			return
		}
		// Enviadas (aceptadas o ignoradas por Last.fm, que no se reintentan).
		scrobbler.queue = scrobbler.queue[len(batch):]
		scrobbler.lastErr = ""
		saveQueue()
		scrobbler.mu.Unlock()
		if raw.Scrobbles.Attr.Ignored > 0 {
			a.libLog("Last.fm: %d escucha(s) ignorada(s) por Last.fm", raw.Scrobbles.Attr.Ignored)
		}
		a.emit("lastfm")
	}
}

// lastfmError es la respuesta de error de la API ({"error": 14, "message": ...}).
type lastfmError struct {
	Code    int    `json:"error"`
	Message string `json:"message"`
}

func (e lastfmError) Error() string { return fmt.Sprintf("Last.fm %d: %s", e.Code, e.Message) }

// lastfmCall hace una llamada firmada. La firma va sobre todos los
// parámetros salvo format; luego se añade format=json.
func lastfmCall(method string, p url.Values, secret string, v any) error {
	p.Set("api_sig", lastfmSig(p, secret))
	p.Set("format", "json")
	req, err := http.NewRequest(method, lastfmBase+"?"+p.Encode(), nil)
	if method == http.MethodPost {
		req, err = http.NewRequest(method, lastfmBase, strings.NewReader(p.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := webClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("Last.fm: HTTP %s", resp.Status)
	}
	var le lastfmError
	if json.Unmarshal(body, &le) == nil && le.Code != 0 {
		return le
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Last.fm: HTTP %s", resp.Status)
	}
	return json.Unmarshal(body, v)
}

// lastfmSig: MD5 de "nombre1valor1nombre2valor2..." (por orden alfabético de
// nombre) seguido del secret.
func lastfmSig(p url.Values, secret string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		if k != "format" && k != "callback" && k != "api_sig" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(p.Get(k))
	}
	b.WriteString(secret)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ---- la cola en disco -------------------------------------------------------

// queuePath: scrobbles.json en la carpeta de datos (lo cambian los tests).
var queuePath = func() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "scrobbles.json")
}

// loadQueue lee la cola la primera vez (con scrobbler.mu cogido).
func loadQueue() {
	if scrobbler.loaded {
		return
	}
	scrobbler.loaded = true
	if data, err := os.ReadFile(queuePath()); err == nil {
		_ = json.Unmarshal(data, &scrobbler.queue)
	}
}

// saveQueue la escribe (con scrobbler.mu cogido); vacía, se borra.
func saveQueue() {
	p := queuePath()
	if p == "" {
		return
	}
	if len(scrobbler.queue) == 0 {
		os.Remove(p)
		return
	}
	if data, err := json.Marshal(scrobbler.queue); err == nil {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, data, 0o644)
	}
}
