package ical

import (
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func buildAndExpand(t *testing.T, fields EventFields) []Occurrence {
	t.Helper()
	data, err := BuildEvent("uid-1", fields, fixedNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cal, err := Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", data, err)
	}
	got, err := Expand(cal, date(t, "2026-11-01T00:00:00Z"), date(t, "2026-12-01T00:00:00Z"), time.UTC)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	return got
}

func TestBuildEventTimed(t *testing.T) {
	amsterdam := location(t, "Europe/Amsterdam")

	got := buildAndExpand(t, EventFields{
		Summary:     "Lunch, then coffee; maybe\\more",
		Location:    "Café, Room 4",
		Description: "line one\nline two",
		Start:       time.Date(2026, 11, 10, 12, 0, 0, 0, amsterdam),
		End:         time.Date(2026, 11, 10, 13, 0, 0, 0, amsterdam),
	})

	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	e := got[0]
	if e.UID != "uid-1" || e.Summary != "Lunch, then coffee; maybe\\more" || e.Location != "Café, Room 4" || e.Description != "line one\nline two" {
		t.Errorf("event = %+v", e)
	}
	if !e.Start.Equal(date(t, "2026-11-10T11:00:00Z")) || !e.End.Equal(date(t, "2026-11-10T12:00:00Z")) || e.AllDay {
		t.Errorf("times = %s to %s, all day %v", e.Start, e.End, e.AllDay)
	}
}

func TestBuildEventAllDay(t *testing.T) {
	got := buildAndExpand(t, EventFields{
		Summary: "Holiday",
		AllDay:  true,
		Start:   time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC),
	})

	if len(got) != 1 || !got[0].AllDay {
		t.Fatalf("got %+v", got)
	}
	if !got[0].Start.Equal(date(t, "2026-11-10T00:00:00Z")) || !got[0].End.Equal(date(t, "2026-11-13T00:00:00Z")) {
		t.Errorf("times = %s to %s", got[0].Start, got[0].End)
	}
}

func TestBuildEventTextCannotInjectProperties(t *testing.T) {
	for name, summary := range map[string]string{
		"CRLF": "Hello\r\nATTENDEE:mailto:evil@example.org",
		"LF":   "Hello\nATTENDEE:mailto:evil@example.org",
		"CR":   "Hello\rATTENDEE:mailto:evil@example.org",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := BuildEvent("uid-1", EventFields{
				Summary: summary,
				Start:   date(t, "2026-11-10T09:00:00Z"),
				End:     date(t, "2026-11-10T10:00:00Z"),
			}, fixedNow)
			if err != nil {
				t.Fatalf("build: %v", err)
			}

			for line := range strings.SplitSeq(string(data), "\n") {
				if strings.HasPrefix(line, "ATTENDEE") {
					t.Errorf("line %q became a property", line)
				}
			}
		})
	}
}

func TestBuildEventIsValidICalendar(t *testing.T) {
	data, err := BuildEvent("uid-1", EventFields{
		Summary: "x",
		Start:   date(t, "2026-11-10T09:00:00Z"),
		End:     date(t, "2026-11-10T10:00:00Z"),
	}, fixedNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	text := string(data)
	for _, want := range []string{"VERSION:2.0\r\n", "PRODID:", "UID:uid-1\r\n", "DTSTAMP:20261105T120000Z\r\n", "DTSTART:20261110T090000Z\r\n", "DTEND:20261110T100000Z\r\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from:\n%s", want, text)
		}
	}
}
