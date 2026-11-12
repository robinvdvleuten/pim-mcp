package ical

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/testtime"
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

	// The step limit bounds what one event may cost. A rule that walks a tenth
	// of it is measured on this machine, so the budget is ten times the worst
	// case the limit allows. Anything slower got past the limit.
	tenth := secondlyFrom(start.Add(-maxRuleSteps / 10 * time.Second))
	budget := 100 * testtime.Fastest(func() {
		cal, err := Parse(tenth)
		if err != nil {
			f.Fatal(err)
		}
		_, _ = Expand(cal, start, end, loc)
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		cal, err := Parse(data)
		if err != nil {
			return
		}
		began := time.Now()
		occurrences, err := Expand(cal, start, end, loc)
		if took := time.Since(began); took > budget {
			t.Fatalf("Expand took %v, the budget is %v", took, budget)
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

// secondlyFrom is a calendar with one event that repeats every second from
// start, so expanding it walks every second up to the range.
func secondlyFrom(start time.Time) []byte {
	return []byte(strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//pim-mcp//test//EN", "BEGIN:VEVENT", "UID:secondly",
		"DTSTAMP:20250101T000000Z", "DTSTART:" + start.UTC().Format(utcFormat),
		"DTEND:" + start.Add(time.Second).UTC().Format(utcFormat), "RRULE:FREQ=SECONDLY", "END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n"))
}
