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

// FindAlbumSourcesSoulseek busca el disco en Soulseek y devuelve, por número
// de pista que falta, los ficheros candidatos ordenados de mejor a peor.
func (a *App) FindAlbumSourcesSoulseek(artist, album string, missing []mbTrack) (map[int][]trackSource, error) {
	if !a.SoulseekState().Connected {
		return nil, fmt.Errorf("Soulseek no está conectado")
	}
	res, err := a.slskSearchWait(strings.TrimSpace(artist+" "+stripParen(album)), 14*time.Second)
	if err != nil {
		return nil, err
	}
	return matchSoulseekCandidates(res, album, missing), nil
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
				ext := strings.ToLower(filepath.Ext(f.Name))
				if _, ok := audioExtRank[ext]; !ok || f.Locked {
					continue
				}
				sim := similarity(trackTitleOf(localTrack{Name: f.Name}), t.Title)
				diff := f.Length - t.Length
				durOK := t.Length > 0 && f.Length > 0 && math.Abs(diff) <= 3
				if sim < 0.5 && !durOK {
					continue
				}
				dur := 0.5
				if t.Length > 0 && f.Length > 0 {
					dur = math.Max(0, 1-math.Max(0, math.Abs(diff)-3)/57)
				}
				score := 0.6*dur + 0.4*sim + audioExtRank[ext]
				if u.FreeSlot {
					score += 0.15
				}
				if ext == ".mp3" && f.BitRate >= 320 {
					score += 0.05
				}
				if similarity(f.Folder, album) > 0.6 {
					score += 0.1 // la carpeta se llama como el disco
				}
				cands = append(cands, trackSource{
					Source: "soulseek", Title: f.Name, Duration: f.Length, Diff: diff, Sim: sim, Score: score,
					Username: u.Username, Filename: f.Filename, Folder: f.Folder, Size: f.Size, BitRate: f.BitRate,
					Ext: strings.TrimPrefix(ext, "."), FreeSlot: u.FreeSlot, Queue: u.QueueLength,
				})
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

// DownloadTrackSoulseek pide un fichero a un usuario, espera a que llegue y
// lo deja como la pista `number` del álbum: nombre, etiquetas y carátula.
func (a *App) DownloadTrackSoulseek(username string, src trackSource, albumDir, key string, number int) (string, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return "", err
	}
	var track *mbTrack
	for i := range rel.Tracks {
		if rel.Tracks[i].Number == number {
			track = &rel.Tracks[i]
		}
	}
	if track == nil {
		return "", fmt.Errorf("la edición no tiene pista %d", number)
	}
	file := slskFile{Filename: src.Filename, Name: src.Title, Folder: src.Folder, Size: src.Size}
	if _, err := a.SoulseekDownload(username, []slskFile{file}); err != nil {
		return "", err
	}
	a.libLog("pista %d: pedida a %s (%s)", number, username, file.Name)

	// Esperar a la transferencia.
	_, _, _, downloads, _ := a.slskSettings()
	deadline := time.Now().Add(15 * time.Minute)
	lastState := ""
	for time.Now().Before(deadline) {
		if a.ctx.Err() != nil {
			return "", a.ctx.Err()
		}
		time.Sleep(2 * time.Second)
		trs, err := a.SoulseekTransfers()
		if err != nil {
			return "", err
		}
		for _, tr := range trs {
			if tr.Username != username || tr.Name != file.Name {
				continue
			}
			if tr.State != lastState {
				lastState = tr.State
				a.libLog("pista %d: %s", number, tr.State)
			}
			switch {
			case strings.Contains(tr.State, "Succeeded"):
				local := filepath.Join(downloads, file.Folder, file.Name)
				if !fileExists(local) {
					if found := findFile(downloads, file.Name); found != "" {
						local = found
					} else {
						return "", fmt.Errorf("descargada pero no encuentro el fichero en %s", downloads)
					}
				}
				out := filepath.Join(albumDir, fmt.Sprintf("%02d - %s%s", track.Number, safeName(track.Title), filepath.Ext(local)))
				cover := filepath.Join(albumDir, "cover.jpg")
				if !fileExists(cover) {
					cover = ""
				}
				if err := rewrite(a.ctx, local, out, trackTags(rel, *track), cover); err != nil {
					return "", err
				}
				// La carpeta de descarga de slskd, si quedó vacía, sobra.
				if dir := filepath.Dir(local); dir != downloads && countAudio(dir) == 0 {
					os.Remove(dir)
				}
				a.libLog("pista %d lista: %s", number, filepath.Base(out))
				return out, nil
			case strings.Contains(tr.State, "Errored"), strings.Contains(tr.State, "Rejected"), strings.Contains(tr.State, "Cancelled"):
				return "", fmt.Errorf("%s: %s %s", username, tr.State, tr.Error)
			}
		}
	}
	return "", fmt.Errorf("la descarga de %s no ha llegado en 15 minutos (cola del otro usuario)", file.Name)
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
