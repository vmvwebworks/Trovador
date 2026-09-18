package main

import "testing"

func TestSoulseekNameVariants(t *testing.T) {
	cases := []struct{ file, official string }{
		{"04 - Oh, god..flac", "Oh, god."},
		{"04. Cloudkicker - Oh, god..flac", "Oh, god."},
		{"Cloudkicker_-_Oh_God.mp3", "Oh, god."},
		{"04 Oh God (2010 Remaster).flac", "Oh, god."},
		{"Cloudkicker - Beacons - 04 Oh, god..mp3", "Oh, god."},
		{"01 - We Are Going To Invert.flac", "We are going to invert…"},
		{"1. we are going to invert....mp3", "We are going to invert…"},
		{"Cloudkicker-We Are Going To Invert.flac", "We are going to invert…"},
		{"A04 Oh god [Cloudkicker].flac", "Oh, god."},
	}
	for _, c := range cases {
		got := trackTitleOf(localTrack{Name: c.file})
		sim := similarity(got, c.official)
		mark := "ok  "
		if sim < 0.5 {
			mark = "FALLA"
		}
		t.Logf("%s %-45s -> %-28q sim %.2f", mark, c.file, got, sim)
		if sim < 0.5 {
			t.Errorf("%q no se reconoce como %q (sim %.2f)", c.file, c.official, sim)
		}
	}
}
