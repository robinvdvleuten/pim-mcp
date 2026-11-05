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

func location(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestExpandTimezoneGolden(t *testing.T) {
	const amsterdam = "Europe/Amsterdam"
	tests := []struct {
		fixture string
		start   string
		end     string
		user    string // the user's zone, for floating and all-day values
	}{
		{"dst-weekly", "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z", "UTC"},
		{"dst-gap", "2027-03-01T00:00:00Z", "2027-04-01T00:00:00Z", "UTC"},
		{"dst-overlap", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "UTC"},
		{"windows-zone", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z", "UTC"},
		{"vtimezone-outlook", "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z", "UTC"},
		{"vtimezone-fixed", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z", "UTC"},
		{"tzid-prefix", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z", "UTC"},
		{"floating", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z", amsterdam},
		{"all-day", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", amsterdam},
		{"all-day-recurring", "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", amsterdam},
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

			occurrences, err := Expand(cal, date(t, tt.start), date(t, tt.end), location(t, tt.user))
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

func expandLines(t *testing.T, loc *time.Location, start, end string, lines ...string) []Occurrence {
	t.Helper()
	cal, err := Parse([]byte(parseEvent(t, lines...)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := Expand(cal, date(t, start), date(t, end), loc)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	return got
}

func TestUntilAsADateIncludesThatDate(t *testing.T) {
	tests := []struct {
		name  string
		zone  string
		lines []string
		want  int
	}{
		{"timed series", "UTC", []string{"DTSTART:20261110T090000Z", "RRULE:FREQ=DAILY;UNTIL=20261112"}, 3},
		// Midnight in New York is 05:00 UTC, past a UNTIL of midnight UTC.
		{"all-day series, UTC until, west of UTC", "America/New_York", []string{"DTSTART;VALUE=DATE:20261110", "RRULE:FREQ=DAILY;UNTIL=20261112T000000Z"}, 3},
		{"all-day series, date until, west of UTC", "America/New_York", []string{"DTSTART;VALUE=DATE:20261110", "RRULE:FREQ=DAILY;UNTIL=20261112"}, 3},
		{"floating until uses the event's zone", "Europe/Amsterdam", []string{"DTSTART;TZID=America/New_York:20261110T090000", "RRULE:FREQ=DAILY;UNTIL=20261112T090000"}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandLines(t, location(t, tt.zone), "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", tt.lines...)

			if len(got) != tt.want {
				t.Errorf("got %d occurrences, want %d: %+v", len(got), tt.want, got)
			}
		})
	}
}

func TestDurationAcrossClockChange(t *testing.T) {
	// 25 Oct 2026 has 25 hours in Amsterdam.
	const dtstart = "DTSTART;TZID=Europe/Amsterdam:20261024T120000"
	tests := []struct {
		name string
		line string
		want string
	}{
		{"a day in DURATION is nominal", "DURATION:P1D", "2026-10-25T12:00:00+01:00"},
		{"hours in DURATION are exact", "DURATION:PT24H", "2026-10-25T11:00:00+01:00"},
		{"both parts", "DURATION:P1DT1H", "2026-10-25T13:00:00+01:00"},
		{"weeks are nominal", "DURATION:P1W", "2026-10-31T12:00:00+01:00"},
		{"DTEND is taken as written", "DTEND;TZID=Europe/Amsterdam:20261025T120000", "2026-10-25T12:00:00+01:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandLines(t, time.UTC, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", dtstart, tt.line)

			if len(got) != 1 {
				t.Fatalf("got %d occurrences, want 1", len(got))
			}
			if end := got[0].End.Format(time.RFC3339); end != tt.want {
				t.Errorf("end = %s, want %s", end, tt.want)
			}
		})
	}
}

func TestRecurringDTENDKeepsExactDuration(t *testing.T) {
	// RFC 5545 3.8.5.3: DTEND gives every occurrence the same exact length, here
	// 24 hours. The occurrence crossing the change ends an hour earlier by the clock.
	got := expandLines(t, time.UTC, "2026-10-24T10:30:00Z", "2026-10-24T11:00:00Z",
		"DTSTART;TZID=Europe/Amsterdam:20261023T120000",
		"DTEND;TZID=Europe/Amsterdam:20261024T120000",
		"RRULE:FREQ=DAILY;COUNT=2")

	if len(got) != 1 {
		t.Fatalf("got %d occurrences, want 1", len(got))
	}
	if end := got[0].End.Format(time.RFC3339); end != "2026-10-25T11:00:00+01:00" {
		t.Errorf("end = %s", end)
	}
}

func TestAllDayEdges(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		wantEnd string
	}{
		{"no end lasts one day", []string{"DTSTART;VALUE=DATE:20261110"}, "2026-11-11T00:00:00Z"},
		{"end equal to start lasts one day", []string{"DTSTART;VALUE=DATE:20261110", "DTEND;VALUE=DATE:20261110"}, "2026-11-11T00:00:00Z"},
		{"duration in days", []string{"DTSTART;VALUE=DATE:20261110", "DURATION:P3D"}, "2026-11-13T00:00:00Z"},
		{"value type left out", []string{"DTSTART:20261110", "DTEND:20261111"}, "2026-11-11T00:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandLines(t, time.UTC, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", tt.lines...)

			if len(got) != 1 || !got[0].AllDay {
				t.Fatalf("got %+v, want one all-day occurrence", got)
			}
			if end := got[0].End.Format(time.RFC3339); end != tt.wantEnd {
				t.Errorf("end = %s, want %s", end, tt.wantEnd)
			}
		})
	}
}

func TestDateValuedEXDATERemovesThatDayFromTimedSeries(t *testing.T) {
	got := expandLines(t, time.UTC, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z",
		"DTSTART;TZID=Europe/Amsterdam:20261110T090000",
		"RRULE:FREQ=DAILY;COUNT=3",
		"EXDATE;VALUE=DATE:20261111")

	if len(got) != 2 {
		t.Fatalf("got %d occurrences, want 2: %+v", len(got), got)
	}
	if id := got[1].OccurrenceID; id != "20261112T080000Z" {
		t.Errorf("second occurrence = %s", id)
	}
}

func TestAllDayOverrideAndEXDATEMatchByDate(t *testing.T) {
	got := expandLines(t, location(t, "America/New_York"), "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z",
		"DTSTART;VALUE=DATE:20261110",
		"RRULE:FREQ=DAILY;COUNT=3",
		"EXDATE;VALUE=DATE:20261111")

	if len(got) != 2 || got[1].OccurrenceID != "20261112" {
		t.Errorf("got %+v, want occurrences on the 10th and 12th", got)
	}
}

func TestBadZones(t *testing.T) {
	everySecond := strings.Join([]string{
		"BEGIN:VTIMEZONE", "TZID:Hostile", "BEGIN:STANDARD", "DTSTART:19700101T000000",
		"TZOFFSETFROM:+0100", "TZOFFSETTO:+0200", "RRULE:FREQ=SECONDLY", "END:STANDARD", "END:VTIMEZONE",
	}, "\r\n")
	noOffset := strings.Join([]string{
		"BEGIN:VTIMEZONE", "TZID:Broken", "BEGIN:STANDARD", "DTSTART:19700101T000000",
		"END:STANDARD", "END:VTIMEZONE",
	}, "\r\n")
	badOffset := strings.Join([]string{
		"BEGIN:VTIMEZONE", "TZID:Broken", "BEGIN:STANDARD", "DTSTART:19700101T000000",
		"TZOFFSETFROM:+0100", "TZOFFSETTO:soon", "END:STANDARD", "END:VTIMEZONE",
	}, "\r\n")

	tests := []struct {
		name      string
		vtimezone string
		tzid      string
		want      string
	}{
		{"unknown, no definition", "", "Mars/Olympus", "Mars/Olympus"},
		{"the machine's zone isn't a TZID", "", "Local", "Local"},
		{"relative path", "", "../../etc/passwd", "etc/passwd"},
		{"rule repeats too often", everySecond, "Hostile", "Hostile"},
		{"no offsets", noOffset, "Broken", "Broken"},
		{"bad offset", badOffset, "Broken", "Broken"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := parseEvent(t, "DTSTART;TZID="+tt.tzid+":20261110T090000")
			if tt.vtimezone != "" {
				event = strings.Replace(event, "BEGIN:VEVENT", tt.vtimezone+"\r\nBEGIN:VEVENT", 1)
			}
			cal, err := Parse([]byte(event))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			started := time.Now()
			_, err = Expand(cal, date(t, "2026-11-01T00:00:00Z"), date(t, "2026-12-01T00:00:00Z"), time.UTC)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Errorf("took %s", elapsed)
			}
		})
	}
}

func TestLocalZoneHasNoName(t *testing.T) {
	got := expandLines(t, time.Local, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z", "DTSTART:20261110T090000")

	if len(got) != 1 || got[0].Timezone != "" {
		t.Errorf("got %+v, want one occurrence without a zone name", got)
	}
}

func TestRuleStartedCenturiesAgoIsRefused(t *testing.T) {
	cal, err := Parse([]byte(parseEvent(t, "DTSTART:17000101T090000Z", "RRULE:FREQ=YEARLY")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err = Expand(cal, date(t, "2026-01-01T00:00:00Z"), date(t, "2027-01-01T00:00:00Z"), time.UTC)

	if !errors.Is(err, ErrRuleTooOld) {
		t.Errorf("err = %v, want ErrRuleTooOld", err)
	}
}

func TestSecondPassOfRepeatedHourKeepsItsLength(t *testing.T) {
	// 01:30Z is the second 02:30 on 25 Oct 2026 in Amsterdam. Going through the
	// wall clock would turn it into the first.
	got := expandLines(t, time.UTC, "2026-10-25T01:00:00Z", "2026-10-25T02:00:00Z",
		"DTSTART;TZID=Europe/Amsterdam:20261020T090000",
		"DTEND;TZID=Europe/Amsterdam:20261020T093000",
		"RDATE:20261025T013000Z")

	if len(got) != 1 {
		t.Fatalf("got %d occurrences, want 1: %+v", len(got), got)
	}
	if got[0].Start.UTC().Format(time.RFC3339) != "2026-10-25T01:30:00Z" {
		t.Errorf("start = %s", got[0].Start)
	}
	if got[0].End.UTC().Format(time.RFC3339) != "2026-10-25T02:00:00Z" {
		t.Errorf("end = %s, want 30 minutes after the start", got[0].End)
	}
}
