package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Completar un álbum desde Soulseek: una sola búsqueda "artista álbum" y
// se localizan, en las carpetas que ofrecen otros usuarios, las pistas que
// faltan (por título y duración). Descargar una la pide, espera a que
// llegue y la deja curada en la carpeta del álbum.

var audioExtRank = map[string]float64{".flac": 0.12, ".wv": 0.1, ".ape": 0.1, ".m4a": 0.03, ".ogg": 0.02, ".opus": 0.02, ".mp3": 0}

// slskAlbum es la carpeta de un usuario con (casi) todo lo que falta. Bajarla
// entera da un disco coherente: misma fuente, mismo formato, misma
// masterización, y una sola cola que esperar.
type slskAlbum struct {
	Username string              `json:"username"`
	Folder   string              `json:"folder"` // ruta remota de la carpeta
	Ext      string              `json:"ext"`    // formato (el de la mayoría de sus pistas)
	BitRate  int                 `json:"bitRate"`
	FreeSlot bool                `json:"freeSlot"`
	Queue    int                 `json:"queue"`
	Size     int64               `json:"size"`    // lo que ocupan las pistas elegidas
	Covered  int                 `json:"covered"` // pistas que faltan que tiene
	Of       int                 `json:"of"`
	Picks    map[int]trackSource `json:"picks"` // número de pista -> su fichero en la carpeta
	Score    float64             `json:"score"`
}

// slskSources: lo que encuentra la búsqueda del disco en Soulseek.
type slskSources struct {
	Tracks map[int][]trackSource `json:"tracks"` // por pista, de mejor a peor (de cualquier carpeta)
	Albums []slskAlbum           `json:"albums"` // carpetas con el disco, la mejor primero
}

// FindAlbumSourcesSoulseek busca el disco en Soulseek: las carpetas que lo
// tienen entero (o casi) y, por número de pista que falta, los ficheros
// candidatos ordenados de mejor a peor.
func (a *App) FindAlbumSourcesSoulseek(artist, album string, missing []mbTrack) (slskSources, error) {
	if !a.SoulseekState().Connected {
		return slskSources{}, fmt.Errorf("Soulseek no está conectado")
	}
	res, err := a.slskSearchWait(strings.TrimSpace(artist+" "+stripParen(album)), 14*time.Second)
	if err != nil {
		return slskSources{}, err
	}
	out := slskSources{Tracks: matchSoulseekCandidates(res, album, missing), Albums: matchSoulseekAlbums(res, album, missing)}
	// Las pistas de la mejor carpeta, las primeras de su lista: así lo
	// marcado por defecto sale todo del mismo sitio.
	if len(out.Albums) > 0 {
		for n, pick := range out.Albums[0].Picks {
			list := []trackSource{pick}
			for _, c := range out.Tracks[n] {
				if c.Username != pick.Username || c.Filename != pick.Filename {
					list = append(list, c)
				}
			}
			out.Tracks[n] = list
		}
	}
	return out, nil
}

// soulseekScore puntúa un fichero como candidato a una pista: duración y
// título, formato sin pérdida, hueco libre y carpeta con el nombre del disco.
func soulseekScore(u slskResponse, f slskFile, t mbTrack, album string) (score, sim, diff float64, ok bool) {
	ext := strings.ToLower(filepath.Ext(f.Name))
	if _, known := audioExtRank[ext]; !known || f.Locked {
		return 0, 0, 0, false
	}
	sim = similarity(trackTitleOf(localTrack{Name: f.Name}), t.Title)
	diff = f.Length - t.Length
	durOK := t.Length > 0 && f.Length > 0 && math.Abs(diff) <= 3
	if sim < 0.5 && !durOK {
		return 0, sim, diff, false
	}
	dur := 0.5
	if t.Length > 0 && f.Length > 0 {
		dur = math.Max(0, 1-math.Max(0, math.Abs(diff)-3)/57)
	}
	score = 0.6*dur + 0.4*sim + audioExtRank[ext]
	if u.FreeSlot {
		score += 0.15
	}
	if ext == ".mp3" && f.BitRate >= 320 {
		score += 0.05
	}
	if similarity(f.Folder, album) > 0.6 {
		score += 0.1 // la carpeta se llama como el disco
	}
	return score, sim, diff, true
}

func soulseekSource(u slskResponse, f slskFile, score, sim, diff float64) trackSource {
	return trackSource{
		Source: "soulseek", Title: f.Name, Duration: f.Length, Diff: diff, Sim: sim, Score: score,
		Username: u.Username, Filename: f.Filename, Folder: f.Folder, Size: f.Size, BitRate: f.BitRate,
		Ext: strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Name)), "."), FreeSlot: u.FreeSlot, Queue: u.QueueLength,
	}
}

// remoteDir: la carpeta de una ruta remota de Soulseek (separador \ o /).
func remoteDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return ""
}

// matchSoulseekAlbums agrupa los ficheros por carpeta de cada usuario y
// empareja en cada una las pistas que faltan, un fichero por pista y sin
// repetir. Aquí se es más exigente que pista a pista: tiene que cuadrar la
// duración (±3 s) o, si no se sabe, el título casi exacto; una carpeta con
// un directo o una versión distinta no debe pasar por el disco.
func matchSoulseekAlbums(res slskSearch, album string, missing []mbTrack) []slskAlbum {
	var out []slskAlbum
	for _, u := range res.Responses {
		byDir := map[string][]slskFile{}
		var dirs []string
		for _, f := range u.Files {
			d := remoteDir(f.Filename)
			if _, ok := byDir[d]; !ok {
				dirs = append(dirs, d)
			}
			byDir[d] = append(byDir[d], f)
		}
		for _, d := range dirs {
			type pair struct {
				track int // índice en missing
				file  int
				src   trackSource
			}
			files := byDir[d]
			var pairs []pair
			for ti, t := range missing {
				for fi, f := range files {
					score, sim, diff, ok := soulseekScore(u, f, t, album)
					if !ok {
						continue
					}
					known := t.Length > 0 && f.Length > 0
					if (known && math.Abs(diff) > 3) || (!known && sim < 0.8) {
						continue
					}
					pairs = append(pairs, pair{ti, fi, soulseekSource(u, f, score, sim, diff)})
				}
			}
			sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].src.Score > pairs[j].src.Score })
			usedT, usedF := map[int]bool{}, map[int]bool{}
			al := slskAlbum{Username: u.Username, Folder: d, FreeSlot: u.FreeSlot, Queue: u.QueueLength, Of: len(missing), Picks: map[int]trackSource{}}
			exts := map[string]int{}
			for _, p := range pairs {
				if usedT[p.track] || usedF[p.file] {
					continue
				}
				usedT[p.track], usedF[p.file] = true, true
				al.Picks[missing[p.track].Number] = p.src
				al.Size += p.src.Size
				al.Score += p.src.Score
				exts[p.src.Ext]++
				al.BitRate = max(al.BitRate, p.src.BitRate)
			}
			al.Covered = len(al.Picks)
			// Una carpeta con dos pistas sueltas no es «el disco».
			if al.Covered == 0 || (al.Covered < al.Of && (al.Covered < 2 || float64(al.Covered) < 0.6*float64(al.Of))) {
				continue
			}
			for e, n := range exts {
				if n > exts[al.Ext] {
					al.Ext = e
				}
			}
			al.Score /= float64(al.Covered)
			out = append(out, al)
		}
	}
	// Primero las que lo tienen todo; luego por calidad, hueco y cola.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Covered != out[j].Covered {
			return out[i].Covered > out[j].Covered
		}
		return out[i].Score-0.002*float64(out[i].Queue) > out[j].Score-0.002*float64(out[j].Queue)
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// matchSoulseekCandidates empareja las pistas que faltan con los ficheros
// que ofrecen los usuarios de una búsqueda: por título y duración, con
// preferencia por formatos sin pérdida, hueco libre y carpetas que se
// llaman como el disco.
func matchSoulseekCandidates(res slskSearch, album string, missing []mbTrack) map[int][]trackSource {
	out := map[int][]trackSource{}
	for _, t := range missing {
		var cands []trackSource
		for _, u := range res.Responses {
			for _, f := range u.Files {
				if score, sim, diff, ok := soulseekScore(u, f, t, album); ok {
					cands = append(cands, soulseekSource(u, f, score, sim, diff))
				}
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
		if len(cands) > 6 {
			cands = cands[:6]
		}
		out[t.Number] = cands
	}
	return out
}

// slskSearchWait lanza una búsqueda y espera a que termine (o a que pase el
// tiempo dado) devolviendo lo que haya.
func (a *App) slskSearchWait(text string, maxWait time.Duration) (slskSearch, error) {
	id, err := a.SoulseekSearch(text)
	if err != nil {
		return slskSearch{}, err
	}
	deadline := time.Now().Add(maxWait)
	var res slskSearch
	for {
		time.Sleep(2 * time.Second)
		res, err = a.SoulseekResults(id)
		if err != nil {
			return res, err
		}
		if res.Complete || time.Now().After(deadline) {
			return res, nil
		}
	}
}

// slskPick: una pista del álbum y el fichero de Soulseek elegido para ella.
type slskPick struct {
	Number int         `json:"number"`
	Src    trackSource `json:"src"`
}

// DownloadTrackSoulseek pide un fichero a un usuario, espera a que llegue y
// lo deja como la pista `number` del álbum: nombre, etiquetas y carátula.
func (a *App) DownloadTrackSoulseek(username string, src trackSource, albumDir, key string, number int) (string, error) {
	paths, errs := a.fetchSoulseek(username, []slskPick{{Number: number, Src: src}}, albumDir, key)
	if err := errs[number]; err != nil {
		return "", err
	}
	return paths[number], nil
}

// DownloadTracksSoulseek pide de golpe a un usuario todas las pistas
// elegidas de su carpeta (una sola cola en su lado) y las va dejando
// curadas según llegan. Devuelve cuántas han llegado; falla solo si
// ninguna.
func (a *App) DownloadTracksSoulseek(username string, picks []slskPick, albumDir, key string) (int, error) {
	paths, errs := a.fetchSoulseek(username, picks, albumDir, key)
	if len(paths) == 0 && len(errs) > 0 {
		var msgs []string
		for _, p := range picks {
			if err := errs[p.Number]; err != nil {
				msgs = append(msgs, fmt.Sprintf("pista %d: %v", p.Number, err))
			}
		}
		return 0, fmt.Errorf("%s", strings.Join(dedupe(msgs), "; "))
	}
	return len(paths), nil
}

// fetchSoulseek pide los ficheros y espera a las transferencias. Se da por
// perdida una pista cuando lleva 15 minutos sin que nada se mueva (la cola
// del otro usuario).
func (a *App) fetchSoulseek(username string, picks []slskPick, albumDir, key string) (paths map[int]string, errs map[int]error) {
	paths, errs = map[int]string{}, map[int]error{}
	failAll := func(err error) (map[int]string, map[int]error) {
		for _, p := range picks {
			errs[p.Number] = err
		}
		return paths, errs
	}
	rel, err := a.GetRelease(key)
	if err != nil {
		return failAll(err)
	}
	tracks := map[int]mbTrack{}
	for _, t := range rel.Tracks {
		tracks[t.Number] = t
	}
	pending := map[string]slskPick{} // por nombre de fichero, que es como lo enseña slskd
	var files []slskFile
	for _, p := range picks {
		if _, ok := tracks[p.Number]; !ok {
			errs[p.Number] = fmt.Errorf("la edición no tiene pista %d", p.Number)
			continue
		}
		files = append(files, slskFile{Filename: p.Src.Filename, Name: p.Src.Title, Folder: p.Src.Folder, Size: p.Src.Size})
		pending[p.Src.Title] = p
	}
	if len(files) == 0 {
		return paths, errs
	}
	if _, err := a.SoulseekDownload(username, files); err != nil {
		return failAll(err)
	}
	a.libLog("Soulseek: %d pista(s) pedidas a %s", len(files), username)

	_, _, _, downloads, _ := a.slskSettings()
	deadline := time.Now().Add(15 * time.Minute)
	states := map[string]string{}
	for len(pending) > 0 && time.Now().Before(deadline) {
		if a.ctx.Err() != nil {
			return failAll(a.ctx.Err())
		}
		time.Sleep(2 * time.Second)
		trs, err := a.SoulseekTransfers()
		if err != nil {
			return failAll(err)
		}
		for _, tr := range trs {
			p, ok := pending[tr.Name]
			if !ok || tr.Username != username {
				continue
			}
			if tr.State != states[tr.Name] || tr.Percent > 0 && strings.Contains(tr.State, "InProgress") {
				deadline = time.Now().Add(15 * time.Minute) // se mueve: hay que seguir esperando
			}
			if tr.State != states[tr.Name] {
				states[tr.Name] = tr.State
				a.libLog("pista %d: %s", p.Number, tr.State)
			}
			switch {
			case strings.Contains(tr.State, "Succeeded"):
				delete(pending, tr.Name)
				out, err := a.curateSoulseek(downloads, p.Src, rel, tracks[p.Number], albumDir)
				if err != nil {
					errs[p.Number] = err
					continue
				}
				paths[p.Number] = out
				a.libLog("pista %d lista: %s", p.Number, filepath.Base(out))
			case strings.Contains(tr.State, "Errored"), strings.Contains(tr.State, "Rejected"), strings.Contains(tr.State, "Cancelled"):
				delete(pending, tr.Name)
				errs[p.Number] = fmt.Errorf("%s: %s %s", username, tr.State, tr.Error)
			}
		}
	}
	for name, p := range pending {
		errs[p.Number] = fmt.Errorf("%s no ha llegado en 15 minutos sin moverse (cola de %s)", name, username)
	}
	return paths, errs
}

// curateSoulseek deja el fichero bajado como la pista del álbum: nombre,
// etiquetas y carátula.
func (a *App) curateSoulseek(downloads string, src trackSource, rel mbReleaseDetail, track mbTrack, albumDir string) (string, error) {
	local := filepath.Join(downloads, src.Folder, src.Title)
	if !fileExists(local) {
		found := findFile(downloads, src.Title)
		if found == "" {
			return "", fmt.Errorf("descargada pero no encuentro el fichero en %s", downloads)
		}
		local = found
	}
	out := filepath.Join(albumDir, fmt.Sprintf("%02d - %s%s", track.Number, safeName(track.Title), filepath.Ext(local)))
	cover := filepath.Join(albumDir, "cover.jpg")
	if !fileExists(cover) {
		cover = ""
	}
	if err := rewrite(a.ctx, local, out, trackTags(rel, track), cover); err != nil {
		return "", err
	}
	// La carpeta de descarga de slskd, si quedó vacía, sobra.
	if dir := filepath.Dir(local); dir != downloads && countAudio(dir) == 0 {
		os.Remove(dir)
	}
	return out, nil
}

// findFile busca un fichero por nombre bajo root (dos niveles).
func findFile(root, name string) string {
	var found string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !d.IsDir() && d.Name() == name {
			found = p
		}
		return nil
	})
	return found
}
