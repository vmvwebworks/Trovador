//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Soulseek de verdad: entra en la red, busca y baja un fichero. Solo con
// SLSK_USER/SLSK_PASS en el entorno; si no, se salta.
func TestSoulseek(t *testing.T) {
	user, pass := os.Getenv("SLSK_USER"), os.Getenv("SLSK_PASS")
	if user == "" || pass == "" {
		t.Skip("sin SLSK_USER/SLSK_PASS")
	}
	app := testApp(t)
	dl := t.TempDir()
	share := t.TempDir()
	os.WriteFile(filepath.Join(share, "leeme.txt"), []byte("music_picker test share"), 0o644)
	app.settings = settings{OutDir: dl, SlskUser: user, SlskPass: pass, SlskShare: share, SlskPort: 50311}
	defer slsk.stop()

	st, err := app.SoulseekConnect()
	if err != nil {
		t.Fatal(err, st.Message)
	}
	t.Log("estado:", st.Message)

	id, err := app.SoulseekSearch("cloudkicker beacons")
	if err != nil {
		t.Fatal(err)
	}
	var res slskSearch
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		if res, err = app.SoulseekResults(id); err != nil {
			t.Fatal(err)
		}
		if res.Complete || len(res.Responses) >= 10 {
			break
		}
	}
	t.Logf("búsqueda: %d usuarios, %d ficheros, estado %s", len(res.Responses), res.Files, res.State)
	if len(res.Responses) == 0 {
		t.Fatal("sin resultados")
	}

	// El primer usuario con hueco libre que tenga un mp3 pequeño.
	var pick *slskResponse
	var file slskFile
	for i := range res.Responses {
		r := &res.Responses[i]
		if !r.FreeSlot {
			continue
		}
		for _, f := range r.Files {
			if strings.HasSuffix(strings.ToLower(f.Name), ".mp3") && f.Size < 6_000_000 {
				pick, file = r, f
				break
			}
		}
		if pick != nil {
			break
		}
	}
	if pick == nil {
		t.Skip("ningún usuario con hueco libre y un mp3 pequeño ahora mismo")
	}
	t.Logf("pidiendo a %s: %s (%d bytes)", pick.Username, file.Name, file.Size)
	if _, err := app.SoulseekDownload(pick.Username, []slskFile{file}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		time.Sleep(3 * time.Second)
		trs, err := app.SoulseekTransfers()
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range trs {
			t.Logf("  %s %.0f%% %s", tr.State, tr.Percent, tr.Name)
			if strings.Contains(tr.State, "Succeeded") {
				var got []string
				filepath.WalkDir(dl, func(p string, d os.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						rel, _ := filepath.Rel(dl, p)
						got = append(got, rel)
					}
					return nil
				})
				t.Log("descargado:", got)
				return
			}
			if strings.Contains(tr.State, "Errored") || strings.Contains(tr.State, "Rejected") {
				t.Fatalf("transferencia fallida: %s %s", tr.State, tr.Error)
			}
		}
	}
	t.Fatal("la descarga no terminó a tiempo")
}

func TestCompleteFromSoulseek(t *testing.T) {
	user, pass := os.Getenv("SLSK_USER"), os.Getenv("SLSK_PASS")
	if user == "" || pass == "" {
		t.Skip("sin SLSK_USER/SLSK_PASS")
	}
	app := testApp(t)
	dl := t.TempDir()
	share := t.TempDir()
	os.WriteFile(filepath.Join(share, "leeme.txt"), []byte("music_picker test share"), 0o644)
	app.settings = settings{OutDir: dl, SlskUser: user, SlskPass: pass, SlskShare: share, SlskPort: 50311}
	defer slsk.stop()
	if _, err := app.SoulseekConnect(); err != nil {
		t.Fatal(err)
	}

	res, _ := app.SearchReleases("Cloudkicker", "Beacons")
	var mbID string
	for _, r := range res.Results {
		if r.Source == srcMusicBrainz && r.TrackCount == 11 {
			mbID = r.ID
			break
		}
	}
	rel, err := app.GetRelease(mbID)
	if err != nil {
		t.Fatal(err)
	}
	album := filepath.Join(dl, "Cloudkicker - Beacons (2010)")
	os.MkdirAll(album, 0o755)
	missing, _ := app.missingTracks(album, rel)
	if len(missing) != 11 {
		t.Fatalf("deberían faltar 11, faltan %d", len(missing))
	}

	found, err := app.FindAlbumSourcesSoulseek("Cloudkicker", "Beacons", missing)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tr := range missing {
		c := found[tr.Number]
		if len(c) > 0 {
			n++
			t.Logf("pista %2d %-40s -> %s [%s %s%dk] %s (%+.0f s, sim %.2f, hueco %v)", tr.Number, tr.Title, c[0].Username, c[0].Ext, "", c[0].BitRate, fmtDur(c[0].Duration), c[0].Diff, c[0].Sim, c[0].FreeSlot)
		}
	}
	t.Logf("candidatos de Soulseek para %d de 11 pistas", n)
	if n < 6 {
		t.Fatalf("pocas pistas localizadas en Soulseek: %d", n)
	}

	// Descargar la primera pista que tenga candidato con hueco libre.
	for _, tr := range missing {
		var pick *trackSource
		for i := range found[tr.Number] {
			if found[tr.Number][i].FreeSlot {
				pick = &found[tr.Number][i]
				break
			}
		}
		if pick == nil {
			continue
		}
		out, err := app.DownloadTrackSoulseek(pick.Username, *pick, album, mbID, tr.Number)
		if err != nil {
			t.Fatal("DownloadTrackSoulseek:", err)
		}
		f, _ := probe(app.ctx, out)
		t.Logf("curada: %s title=%q track=%q dur=%s cover=%v", filepath.Base(out), f.Title, f.Track, fmtDur(f.Duration), f.HasCover)
		if f.Title != tr.Title {
			t.Errorf("título mal: %q", f.Title)
		}
		left, _ := app.missingTracks(album, rel)
		if len(left) != 10 {
			t.Errorf("tras descargar deberían faltar 10, faltan %d", len(left))
		}
		return
	}
	t.Skip("ningún candidato con hueco libre ahora mismo")
}
