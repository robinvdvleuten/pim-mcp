package ical

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func date(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// TestExpandGolden compares against hand-checked files, so a wrong occurrence
// can't pass by being generated from the code under test.
func TestExpandGolden(t *testing.T) {
	tests := []struct {
		fixture string
		start   string
		end     string
	}{
		{"single-event", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z"},
		{"daily-count", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		{"weekly-until", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		// February and April have no 31st, so both are skipped.
		{"monthly-day-31", "2026-12-01T00:00:00Z", "2027-05-01T00:00:00Z"},
		// Started six years ago. The range opens during an occurrence, and
		// the next one starts exactly when the range closes.
		{"endless-weekly", "2026-11-10T00:00:00Z", "2026-11-16T23:00:00Z"},
		{"two-events", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ics", tt.fixture+".ics"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", tt.fixture+".json"))
			if err != nil {
				t.Fatal(err)
			}
			cal, err := Parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			occurrences, err := Expand(cal, date(t, tt.start), date(t, tt.end))
			if err != nil {
				t.Fatalf("expand: %v", err)
			}

			got, err := json.MarshalIndent(occurrences, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(want)) != string(got) {
				t.Errorf("occurrences differ\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func parseEvent(t *testing.T, lines ...string) string {
	t.Helper()
	all := append([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//pim-mcp//test//EN",
		"BEGIN:VEVENT", "UID:test@example.org", "DTSTAMP:20261101T120000Z",
	}, lines...)
	all = append(all, "END:VEVENT", "END:VCALENDAR", "")
	return strings.Join(all, "\r\n")
}

func TestExpandBoundaries(t *testing.T) {
	windowStart := date(t, "2026-11-10T10:00:00Z")
	windowEnd := date(t, "2026-11-10T12:00:00Z")

	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{"ends when the range opens", []string{"DTSTART:20261110T090000Z", "DTEND:20261110T100000Z"}, 0},
		{"overlaps the start of the range", []string{"DTSTART:20261110T090000Z", "DTEND:20261110T100001Z"}, 1},
		{"starts when the range closes", []string{"DTSTART:20261110T120000Z", "DTEND:20261110T130000Z"}, 0},
		{"spans the whole range", []string{"DTSTART:20261110T090000Z", "DTEND:20261110T130000Z"}, 1},
		{"no end, inside", []string{"DTSTART:20261110T100000Z"}, 1},
		{"no end, at the range close", []string{"DTSTART:20261110T120000Z"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal, err := Parse([]byte(parseEvent(t, tt.lines...)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			got, err := Expand(cal, windowStart, windowEnd)
			if err != nil {
				t.Fatalf("expand: %v", err)
			}

			if len(got) != tt.want {
				t.Errorf("got %d occurrences, want %d: %+v", len(got), tt.want, got)
			}
		})
	}
}

func TestExpandLimits(t *testing.T) {
	daily := parseEvent(t, "DTSTART:20200101T090000Z", "RRULE:FREQ=DAILY")
	everyMinute := parseEvent(t, "DTSTART:20261101T000000Z", "RRULE:FREQ=MINUTELY")
	secondlySinceLongAgo := parseEvent(t, "DTSTART:20000101T000000Z", "RRULE:FREQ=SECONDLY")

	tests := []struct {
		name  string
		event string
		start string
		end   string
		want  error
	}{
		{"exactly 366 days is allowed", daily, "2026-01-01T00:00:00Z", "2027-01-02T00:00:00Z", nil},
		{"over 366 days", daily, "2026-01-01T00:00:00Z", "2027-01-02T00:00:01Z", ErrRangeTooLarge},
		{"end before start", daily, "2026-02-01T00:00:00Z", "2026-01-01T00:00:00Z", ErrInvalidRange},
		{"empty range", daily, "2026-02-01T00:00:00Z", "2026-02-01T00:00:00Z", ErrInvalidRange},
		{"over 1000 occurrences", everyMinute, "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", ErrTooManyOccurrences},
		{"rule too expensive to walk", secondlySinceLongAgo, "2026-11-01T00:00:00Z", "2026-11-01T00:00:01Z", ErrRuleTooExpensive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal, err := Parse([]byte(tt.event))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			_, err = Expand(cal, date(t, tt.start), date(t, tt.end))

			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestExpandRejectsBadEvents(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"no start", []string{"SUMMARY:x"}, "DTSTART"},
		{"bad rule", []string{"DTSTART:20261110T090000Z", "RRULE:FREQ=NEVER"}, "test@example.org"},
		{"impossible rule value", []string{"DTSTART:20261110T090000Z", "RRULE:FREQ=DAILY;BYHOUR=99"}, "test@example.org"},
		{"end before start", []string{"DTSTART:20261110T090000Z", "DTEND:20261110T080000Z"}, "test@example.org"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal, err := Parse([]byte(parseEvent(t, tt.lines...)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			_, err = Expand(cal, date(t, "2026-11-01T00:00:00Z"), date(t, "2026-12-01T00:00:00Z"))

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for name, data := range map[string]string{
		"not ical": "hello world",
		"empty":    "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Error("parse succeeded")
			}
		})
	}
}
