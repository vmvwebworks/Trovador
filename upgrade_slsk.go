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

// Mejorar la calidad de un disco desde Soulseek: buscar el álbum, y por cada
// pista que tenemos, ofrecer versiones claramente mejores (sin pérdida frente
// a mp3, o bastante más bitrate). Sustituir una descarga la nueva, la deja
// curada y aparta la antigua a _original\ (no se borra nada).

// upgrade es una pista local con sus posibles mejoras.
type upgrade struct {
	File       int           `json:"file"` // índice en los ficheros de la carpeta
	Name       string        `json:"name"`
	Codec      string        `json:"codec"`
	Bitrate    int           `json:"bitrate"`
	Duration   float64       `json:"duration"`
	Track      *mbTrack      `json:"track,omitempty"` // pista de la edición a la que corresponde
	Candidates []trackSource `json:"candidates"`      // de mejor a peor; vacío = ya está en su mejor versión
}

// qualityRank: número comparable entre formatos. Sin pérdida vale más que
// cualquier bitrate con pérdida.
func qualityRank(codecOrExt string, bitrate int) float64 {
	switch strings.ToLower(strings.TrimPrefix(codecOrExt, ".")) {
	case "flac", "wv", "wavpack", "ape", "alac", "wav", "pcm_s16le", "pcm_s24le":
		return 10000
	}
	return float64(bitrate)
}

// isUpgrade: la candidata mejora claramente la pista local.
func isUpgrade(local localTrack, c trackSource) bool {
	have, want := qualityRank(local.Codec, local.Bitrate), qualityRank(c.Ext, c.BitRate)
	if want >= 10000 {
		return have < 10000
	}
	if have >= 10000 || want == 0 {
		return false
	}
	// Un salto que se note: al menos ×1,4 y 64 kbps (128→192 sí, 256→320 no).
	return want >= have*1.4 && want-have >= 64
}

// FindUpgradesSoulseek busca el disco en Soulseek y, por cada pista local,
// devuelve las versiones mejores que hay (o ninguna).
func (a *App) FindUpgradesSoulseek(dir, key string) ([]upgrade, error) {
	if !a.SoulseekState().Connected {
		return nil, fmt.Errorf("Soulseek no está conectado")
	}
	rel, err := a.GetRelease(key)
	if err != nil {
		return nil, err
	}
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return nil, err
	}
	res, err := a.slskSearchWait(strings.TrimSpace(rel.Release.Artist+" "+stripParen(rel.Release.Title)), 14*time.Second)
	if err != nil {
		return nil, err
	}

	// Qué pista de la edición es cada fichero (para el título oficial).
	pairs := matchTracks(scan.Files, rel.Tracks)
	trackOf := map[int]*mbTrack{}
	for _, p := range pairs {
		if p.File >= 0 {
			t := rel.Tracks[p.Track]
			trackOf[p.File] = &t
		}
	}

	var out []upgrade
	for i, f := range scan.Files {
		u := upgrade{File: i, Name: f.Name, Codec: f.Codec, Bitrate: f.Bitrate, Duration: f.Duration, Track: trackOf[i], Candidates: []trackSource{}}
		title := trackTitleOf(f)
		if u.Track != nil {
			title = u.Track.Title
		}
		for _, r := range res.Responses {
			for _, rf := range r.Files {
				ext := strings.ToLower(filepath.Ext(rf.Name))
				if _, ok := audioExtRank[ext]; !ok || rf.Locked || rf.Length == 0 {
					continue
				}
				diff := rf.Length - f.Duration
				if math.Abs(diff) > 3 {
					continue
				}
				sim := similarity(trackTitleOf(localTrack{Name: rf.Name}), title)
				if sim < 0.5 && math.Abs(diff) > 1 {
					continue
				}
				c := trackSource{
					Source: "soulseek", Title: rf.Name, Duration: rf.Length, Diff: diff, Sim: sim,
					Username: r.Username, Filename: rf.Filename, Folder: rf.Folder, Size: rf.Size, BitRate: rf.BitRate,
					Ext: strings.TrimPrefix(ext, "."), FreeSlot: r.FreeSlot, Queue: r.QueueLength,
				}
				if !isUpgrade(f, c) {
					continue
				}
				c.Score = qualityRank(c.Ext, c.BitRate)/100 + 0.4*sim
				if r.FreeSlot {
					c.Score += 5
				}
				if similarity(rf.Folder, rel.Release.Title) > 0.6 {
					c.Score += 1
				}
				u.Candidates = append(u.Candidates, c)
			}
		}
		sort.SliceStable(u.Candidates, func(x, y int) bool { return u.Candidates[x].Score > u.Candidates[y].Score })
		if len(u.Candidates) > 4 {
			u.Candidates = u.Candidates[:4]
		}
		out = append(out, u)
	}
	return out, nil
}

// ReplaceTrackSoulseek sustituye un fichero local por una versión mejor de
// Soulseek: aparta el actual a _original\, descarga y cura la nueva. Si la
// descarga falla, devuelve el original a su sitio.
func (a *App) ReplaceTrackSoulseek(dir, key string, fileIdx int, src trackSource) (string, error) {
	rel, err := a.GetRelease(key)
	if err != nil {
		return "", err
	}
	scan, err := a.ScanFolder(dir)
	if err != nil {
		return "", err
	}
	if fileIdx < 0 || fileIdx >= len(scan.Files) {
		return "", fmt.Errorf("fichero fuera de rango")
	}
	old := scan.Files[fileIdx]
	number := 0
	for _, p := range matchTracks(scan.Files, rel.Tracks) {
		if p.File == fileIdx {
			number = rel.Tracks[p.Track].Number
		}
	}
	if number == 0 {
		return "", fmt.Errorf("%s no corresponde a ninguna pista de la edición", old.Name)
	}

	orig := filepath.Join(dir, "_original")
	if err := os.MkdirAll(orig, 0o755); err != nil {
		return "", err
	}
	parked := filepath.Join(orig, old.Name)
	if err := os.Rename(old.Path, parked); err != nil {
		return "", fmt.Errorf("no puedo apartar %s: %w", old.Name, err)
	}
	a.libLog("sustituyendo %s (%s %dk) por %s de %s", old.Name, old.Codec, old.Bitrate, strings.ToUpper(src.Ext), src.Username)

	out, err := a.DownloadTrackSoulseek(src.Username, src, dir, key, number)
	if err != nil {
		if e := os.Rename(parked, old.Path); e != nil {
			a.libLog("aviso: no pude devolver %s a su sitio: %v", old.Name, e)
		}
		return "", err
	}
	a.libLog("sustituida: %s -> %s (original en _original\\)", old.Name, filepath.Base(out))
	return out, nil
}
