package main

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// dupeKeys inventa una biblioteca con duplicados de varias formas: con el
// artista delante, con año, con erratas, sin artista, con otro artista.
func dupeKeys(n int) [][2]string {
	r := rand.New(rand.NewSource(1))
	artists := []string{"Oliphant", "Cloudkicker", "Dead Can Dance", "Björk", "Sigur Rós", "AC/DC", "???", ""}
	var keys [][2]string
	for i := 0; len(keys) < n; i++ {
		artist := artists[r.Intn(len(artists))]
		if r.Intn(3) == 0 {
			artist = fmt.Sprintf("Artista %d", r.Intn(n/4+1))
		}
		album := fmt.Sprintf("Songs from the Crusades %d", r.Intn(n/3+1))
		switch r.Intn(6) {
		case 0:
			album = artist + " - " + album
		case 1:
			album += " (1996)"
		case 2:
			album += "e"
		case 3:
			artist = ""
		}
		keys = append(keys, [2]string{artist, album})
	}
	return keys
}

// naiveGroups es el criterio de siempre: sameAlbum con cada pareja.
func naiveGroups(keys [][2]string) [][]int {
	parent := make([]int, len(keys))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if sameAlbum(keys[i][0], keys[i][1], keys[j][0], keys[j][1]) {
				parent[find(i)] = find(j)
			}
		}
	}
	byRoot := map[int][]int{}
	for i := range keys {
		byRoot[find(i)] = append(byRoot[find(i)], i)
	}
	var groups [][]int
	for _, g := range byRoot {
		if len(g) >= 2 {
			groups = append(groups, g)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

// La versión rápida agrupa exactamente igual que comparar pareja a pareja.
func TestGroupSimilarAlbumsMatchesNaive(t *testing.T) {
	keys := dupeKeys(250)
	got, err := groupSimilarAlbums(context.Background(), keys)
	if err != nil {
		t.Fatal(err)
	}
	if want := naiveGroups(keys); !reflect.DeepEqual(got, want) {
		t.Errorf("grupos distintos:\n%v\nquiero\n%v", got, want)
	}
	if len(got) == 0 {
		t.Error("los datos de prueba deberían tener duplicados")
	}
}

// realisticKeys: una biblioteca más normal, con unos 600 artistas distintos y
// títulos variados, y alguna copia repetida.
func realisticKeys(n int) [][2]string {
	r := rand.New(rand.NewSource(2))
	words := []string{"night", "songs", "of", "the", "crusades", "black", "river", "sun", "live", "at",
		"blue", "dream", "winter", "fire", "machine", "lost", "garden", "voices", "stone", "city",
		"silver", "moon", "ghost", "empire", "salt", "light", "echo", "north", "glass", "heart"}
	phrase := func(k int) string {
		var p []string
		for range k {
			p = append(p, words[r.Intn(len(words))])
		}
		return strings.Join(p, " ")
	}
	artists := make([]string, 600)
	for i := range artists {
		artists[i] = strings.Title(phrase(1+r.Intn(2))) + fmt.Sprint(i)
	}
	var keys [][2]string
	for len(keys) < n {
		artist, album := artists[r.Intn(len(artists))], strings.Title(phrase(1+r.Intn(4)))
		keys = append(keys, [2]string{artist, album})
		if r.Intn(10) == 0 {
			keys = append(keys, [2]string{artist, artist + " - " + album + " (2001)"})
		}
	}
	return keys[:n]
}

func TestGroupSimilarAlbumsRealisticMatchesNaive(t *testing.T) {
	keys := realisticKeys(300)
	got, _ := groupSimilarAlbums(context.Background(), keys)
	if want := naiveGroups(keys); !reflect.DeepEqual(got, want) {
		t.Errorf("grupos distintos:\n%v\nquiero\n%v", got, want)
	}
}

func BenchmarkGroupSimilarAlbums(b *testing.B) {
	keys := realisticKeys(2000)
	for b.Loop() {
		groupSimilarAlbums(context.Background(), keys)
	}
}

func BenchmarkGroupSimilarAlbumsWorstCase(b *testing.B) {
	keys := dupeKeys(2000)
	for b.Loop() {
		groupSimilarAlbums(context.Background(), keys)
	}
}
