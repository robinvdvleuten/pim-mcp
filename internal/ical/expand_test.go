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
		golden  string // defaults to fixture
	}{
		{fixture: "single-event", start: "2026-11-10T00:00:00Z", end: "2026-11-11T00:00:00Z"},
		{fixture: "daily-count", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		{fixture: "weekly-until", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		// February and April have no 31st, so both are skipped.
		{fixture: "monthly-day-31", start: "2026-12-01T00:00:00Z", end: "2027-05-01T00:00:00Z"},
		// Started six years ago. The range opens during an occurrence, and
		// the next one starts exactly when the range closes.
		{fixture: "endless-weekly", start: "2026-11-10T00:00:00Z", end: "2026-11-16T23:00:00Z"},
		{fixture: "two-events", start: "2026-11-10T00:00:00Z", end: "2026-11-11T00:00:00Z"},
		{fixture: "exdate", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		{fixture: "override", start: "2026-11-01T00:00:00Z", end: "2027-01-01T00:00:00Z"},
		// The moved occurrence's original slot is in range, its new one isn't.
		{fixture: "override", start: "2026-11-16T00:00:00Z", end: "2026-11-17T00:00:00Z", golden: "override-moved-out"},
		// The other way round: only the new slot is in range.
		{fixture: "override", start: "2026-12-01T00:00:00Z", end: "2026-12-02T00:00:00Z", golden: "override-moved-in"},
		{fixture: "override-cancelled", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		{fixture: "override-orphan", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		{fixture: "rdate", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		{fixture: "rdate-only", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
		{fixture: "two-series", start: "2026-11-01T00:00:00Z", end: "2026-12-01T00:00:00Z"},
	}

	for _, tt := range tests {
		goldenName := tt.golden
		if goldenName == "" {
			goldenName = tt.fixture
		}
		t.Run(goldenName, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ics", tt.fixture+".ics"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", goldenName+".json"))
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
		{"bad EXDATE", []string{"DTSTART:20261110T090000Z", "RRULE:FREQ=DAILY", "EXDATE:tomorrow"}, "EXDATE"},
		{"bad RDATE", []string{"DTSTART:20261110T090000Z", "RDATE:tomorrow"}, "RDATE"},
		{"RDATE period", []string{"DTSTART:20261110T090000Z", "RDATE;VALUE=PERIOD:20261111T090000Z/PT1H"}, "PERIOD"},
		{"bad RECURRENCE-ID", []string{"DTSTART:20261110T090000Z", "RECURRENCE-ID:tomorrow"}, "RECURRENCE-ID"},
		{"override of all later instances", []string{"DTSTART:20261110T090000Z", "RECURRENCE-ID;RANGE=THISANDFUTURE:20261110T090000Z"}, "THISANDFUTURE"},
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

func TestExpandKeepsWholeTextValues(t *testing.T) {
	tests := map[string]struct{ value, want string }{
		"unescaped comma":  {`Lunch, then coffee`, "Lunch, then coffee"},
		"escaped comma":    {`Lunch\, then coffee`, "Lunch, then coffee"},
		"escaped others":   {`a\;b\\c\nd\Ne`, "a;b\\c\nd\ne"},
		"backslash then n": {`a\\nb`, `a\nb`},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cal, err := Parse([]byte(parseEvent(t, "DTSTART:20261110T090000Z", "SUMMARY:"+tt.value)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			got, err := Expand(cal, date(t, "2026-11-10T00:00:00Z"), date(t, "2026-11-11T00:00:00Z"))
			if err != nil {
				t.Fatalf("expand: %v", err)
			}

			if len(got) != 1 || got[0].Summary != tt.want {
				t.Errorf("got %+v, want summary %q", got, tt.want)
			}
		})
	}
}
