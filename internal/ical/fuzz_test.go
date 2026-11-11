package ical

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedInvites adds the shared invite fixtures, which are modelled on real mail.
func seedInvites(f *testing.F) {
	f.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "ics", "*.ics"))
	if err != nil || len(paths) == 0 {
		f.Fatalf("no seed fixtures found: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
}

// Invites come from strangers, so no input may panic, and what an invite is
// stored as has to be readable again.
func FuzzParseInvite(f *testing.F) {
	seedInvites(f)
	loc := time.FixedZone("test", 3600)

	f.Fuzz(func(t *testing.T, data []byte) {
		invite, err := ParseInvite(data, loc)
		if err != nil {
			return
		}
		for _, event := range invite.Events {
			stored, err := invite.Storable(event.UID)
			if err != nil {
				continue
			}
			if _, err := Parse(stored); err != nil {
				t.Fatalf("storable form of %q does not parse: %v\n%s", event.UID, err, stored)
			}
		}
	})
}

// Expanding is bounded in cost and in size whatever rule a stranger writes.
func FuzzExpand(f *testing.F) {
	seedInvites(f)
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		f.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	end := start.AddDate(1, 0, 0)

	f.Fuzz(func(t *testing.T, data []byte) {
		cal, err := Parse(data)
		if err != nil {
			return
		}
		began := time.Now()
		occurrences, err := Expand(cal, start, end, loc)
		if took := time.Since(began); took > 5*time.Second {
			t.Fatalf("Expand took %v", took)
		}
		if err != nil {
			return
		}
		if len(occurrences) > MaxOccurrences {
			t.Fatalf("got %d occurrences, limit is %d", len(occurrences), MaxOccurrences)
		}
		for _, o := range occurrences {
			if o.End.Before(o.Start) {
				t.Fatalf("occurrence %q ends %v before it starts %v", o.UID, o.End, o.Start)
			}
		}
	})
}
