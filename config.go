package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// settings es lo que el usuario configura desde la ventana y se guarda en
// disco. Los campos van en mayúscula (exportados) para que encoding/json
// los vea, y las etiquetas fijan el nombre en el JSON y en el JS.
type settings struct {
	OutDir       string `json:"outDir"`       // carpeta de destino
	Workers      int    `json:"workers"`      // descargas simultáneas
	Format       string `json:"format"`       // mp3, m4a, opus, flac...
	Quality      string `json:"quality"`      // "0" (mejor) o un bitrate como "192K"
	DiscogsToken string `json:"discogsToken"` // token personal de Discogs (opcional)
	LastfmKey    string `json:"lastfmKey"`    // API key de Last.fm para los artistas parecidos (opcional)
	FolderIcons  bool   `json:"folderIcons"`  // crear/actualizar el icono de carpeta del Explorador al aplicar cambios
	Collection   string `json:"collection"`   // carpeta de la colección (reproductor); vacío = la de descargas

	// Scrobbling a Last.fm (scrobble.go): el secret firma las peticiones; la
	// sesión la da Last.fm al autorizar y solo la toca Go.
	LastfmSecret  string `json:"lastfmSecret"`
	LastfmSession string `json:"lastfmSession"`
	LastfmUser    string `json:"lastfmUser"`
	Scrobble      bool   `json:"scrobble"`

	// Soulseek (pestaña Soulseek, motor slskd)
	SlskUser  string `json:"slskUser"`
	SlskPass  string `json:"slskPass"`
	SlskShare string `json:"slskShare"` // carpeta compartida (solo lectura para los demás)
	SlskPort  int    `json:"slskPort"`  // puerto de escucha para las conexiones entrantes
}

func defaultSettings() settings {
	return settings{
		OutDir:      filepath.Join(musicDir(), "Trovador"),
		Workers:     3,
		Format:      "mp3",
		Quality:     "0",
		FolderIcons: true,
	}
}

// musicDir: la carpeta de música del usuario. En Linux se llama según el
// idioma (~/Música, ~/Musik...) y la apunta user-dirs.dirs, que es lo que
// lee xdg-user-dir; en Windows (y si no está) es ~/Music.
func musicDir() string {
	home, _ := os.UserHomeDir()
	if cfg, err := os.UserConfigDir(); err == nil {
		if raw, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				val, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_MUSIC_DIR=")
				if !ok {
					continue
				}
				val = strings.ReplaceAll(strings.Trim(val, `"`), "$HOME", home)
				if filepath.IsAbs(val) && filepath.Clean(val) != home { // "$HOME" a secas = desactivada
					return val
				}
			}
		}
	}
	return filepath.Join(home, "Music")
}

// dataDir: la carpeta de datos de la app, %APPDATA%\Trovador en Windows
// (os.UserConfigDir elige la carpeta correcta en cada sistema): ajustes,
// caché de tarjetas y miniaturas, favoritos, encuadres, datos de slskd. La
// app se llamó music_picker: si queda esa carpeta y no existe la nueva, se
// renombra para no perder nada.
func dataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "Trovador")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if old := filepath.Join(base, "music_picker"); dirExists(old) {
			_ = os.Rename(old, dir)
		}
	}
	return dir, nil
}

// configPath: config.json en la carpeta de datos.
func configPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadSettings lee el fichero si existe; si no, o si está roto, devuelve
// los valores por defecto. Un fallo aquí no debe impedir arrancar la app.
func loadSettings() settings {
	s := defaultSettings()
	path, err := configPath()
	if err != nil {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	// Unmarshal sobre `s` ya rellena: los campos ausentes del JSON
	// conservan el valor por defecto. Si el fichero está roto se aparta
	// como config.json.roto en vez de pisarlo al guardar: ahí siguen la
	// contraseña de Soulseek, el token de Discogs y las carpetas.
	if err := json.Unmarshal(data, &s); err != nil {
		_ = os.Rename(path, path+".roto")
		s = defaultSettings()
	}
	if s.Workers < 1 {
		s.Workers = 1
	}
	return s
}

// save escribe la configuración creando la carpeta si hace falta.
func (s settings) save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 0600: lleva la contraseña de Soulseek, que solo debe leer el usuario.
	return writeFileAtomic(path, data, 0o600)
}

func cacheDir() string {
	if d := os.Getenv("MUSIC_PICKER_CACHE"); d != "" { // los tests no tocan la caché real
		return d
	}
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return dir
}
