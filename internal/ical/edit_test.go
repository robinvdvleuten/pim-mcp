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

func strPtr(s string) *string { return &s }

func calendarOf(t *testing.T, events ...[]string) string {
	t.Helper()
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN"}
	for _, event := range events {
		lines = append(lines, "BEGIN:VEVENT", "UID:series@example.org", "DTSTAMP:20261001T120000Z")
		lines = append(lines, event...)
		lines = append(lines, "END:VEVENT")
	}
	lines = append(lines, "END:VCALENDAR", "")
	return strings.Join(lines, "\r\n")
}

// editSeries applies a patch and expands the stored result, as a later read would.
func editSeries(t *testing.T, data string, patch EventPatch, start, end string) []Occurrence {
	t.Helper()
	out := mustEditSeries(t, data, patch)
	cal, err := Parse(out)
	if err != nil {
		t.Fatalf("parse %s: %v", out, err)
	}
	got, err := Expand(cal, date(t, start), date(t, end), time.UTC)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	return got
}

func mustEditSeries(t *testing.T, data string, patch EventPatch) []byte {
	t.Helper()
	out, err := EditSeries([]byte(data), patch, time.UTC, fixedNow)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	return out
}

var weekly = []string{
	"DTSTART;TZID=Europe/Amsterdam:20261102T100000", "DTEND;TZID=Europe/Amsterdam:20261102T110000",
	"RRULE:FREQ=WEEKLY;COUNT=3", "SUMMARY:Weekly", "LOCATION:Room 1",
}

func summaries(occurrences []Occurrence) string {
	parts := make([]string, len(occurrences))
	for i, o := range occurrences {
		parts[i] = o.Summary + "@" + o.Start.UTC().Format("0102T1504")
	}
	return strings.Join(parts, " ")
}

func TestEditSeriesChangesText(t *testing.T) {
	got := editSeries(t, calendarOf(t, weekly), EventPatch{Summary: strPtr("Renamed, again"), Location: strPtr("")},
		"2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")

	if want := "Renamed, again@1102T0900 Renamed, again@1109T0900 Renamed, again@1116T0900"; summaries(got) != want {
		t.Errorf("got %s, want %s", summaries(got), want)
	}
	if got[0].Location != "" {
		t.Errorf("location = %q, want it cleared", got[0].Location)
	}
}

func TestEditSeriesLeavesWhatItWasNotAskedToChange(t *testing.T) {
	data := strings.Replace(calendarOf(t, weekly), "SUMMARY:Weekly\r\n",
		"SUMMARY:Weekly\r\nX-CUSTOM;A=B:keep me\r\nATTENDEE;CN=Jan:mailto:jan@example.org\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:x\r\nEND:VALARM\r\n", 1)

	out := string(mustEditSeries(t, data, EventPatch{Summary: strPtr("New")}))

	for _, want := range []string{"X-CUSTOM;A=B:keep me", "ATTENDEE;CN=Jan:mailto:jan@example.org", "BEGIN:VALARM", "TRIGGER:-PT15M", "LOCATION:Room 1", "RRULE:FREQ=WEEKLY;COUNT=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was lost:\n%s", want, out)
		}
	}
}

func TestEditSeriesRecordsTheChange(t *testing.T) {
	out := string(mustEditSeries(t, strings.Replace(calendarOf(t, weekly), "SUMMARY:Weekly", "SEQUENCE:4\r\nSUMMARY:Weekly", 1), EventPatch{Summary: strPtr("New")}))

	for _, want := range []string{"SEQUENCE:5\r\n", "DTSTAMP:20261105T120000Z\r\n", "LAST-MODIFIED:20261105T120000Z\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
}

func TestEditSeriesKeepsOverrides(t *testing.T) {
	override := []string{"RECURRENCE-ID;TZID=Europe/Amsterdam:20261109T100000", "DTSTART;TZID=Europe/Amsterdam:20261109T100000", "DTEND;TZID=Europe/Amsterdam:20261109T110000", "SUMMARY:Special"}

	got := editSeries(t, calendarOf(t, weekly, override), EventPatch{Summary: strPtr("Renamed")}, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")

	if want := "Renamed@1102T0900 Special@1109T0900 Renamed@1116T0900"; summaries(got) != want {
		t.Errorf("got %s, want %s", summaries(got), want)
	}
}

func TestEditSeriesMovesTheTimeInTheSeriesZone(t *testing.T) {
	// 14:00 on 2 Nov is 15:00 in Amsterdam, and stays 15:00 across the clock change.
	moved := time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC) // 15:00 summer time
	series := []string{
		"DTSTART;TZID=Europe/Amsterdam:20261019T100000", "DTEND;TZID=Europe/Amsterdam:20261019T110000",
		"RRULE:FREQ=WEEKLY;COUNT=2", "SUMMARY:Weekly",
	}

	got := editSeries(t, calendarOf(t, series), EventPatch{Start: &moved}, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z")

	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Start.Format(time.RFC3339) != "2026-10-19T15:00:00+02:00" || got[1].Start.Format(time.RFC3339) != "2026-10-26T15:00:00+01:00" {
		t.Errorf("starts = %s, %s", got[0].Start.Format(time.RFC3339), got[1].Start.Format(time.RFC3339))
	}
	if got[0].End.Sub(got[0].Start) != time.Hour {
		t.Errorf("length = %s, want it kept", got[0].End.Sub(got[0].Start))
	}
}

func TestEditSeriesMovingCarriesExceptionsAlong(t *testing.T) {
	// One hour later: the instances that are excluded or changed have to stay the
	// same instances, so their identifiers move as well.
	moved := date(t, "2026-11-02T10:00:00Z") // 11:00 in Amsterdam
	inPlace := []string{"RECURRENCE-ID;TZID=Europe/Amsterdam:20261109T100000", "DTSTART;TZID=Europe/Amsterdam:20261109T100000", "DTEND;TZID=Europe/Amsterdam:20261109T110000", "SUMMARY:Same slot"}
	elsewhere := []string{"RECURRENCE-ID:20261116T090000Z", "DTSTART:20261120T150000Z", "DTEND:20261120T160000Z", "SUMMARY:Elsewhere"}
	master := append(append([]string{}, weekly[:2]...), "RRULE:FREQ=WEEKLY;COUNT=5", "SUMMARY:Weekly", "EXDATE;TZID=Europe/Amsterdam:20261123T100000")

	got := editSeries(t, calendarOf(t, master, inPlace, elsewhere), EventPatch{Start: &moved}, "2026-11-01T00:00:00Z", "2026-12-07T00:00:00Z")

	want := "Weekly@1102T1000 Same slot@1109T1000 Elsewhere@1120T1500 Weekly@1130T1000"
	if summaries(got) != want {
		t.Errorf("got %s, want %s", summaries(got), want)
	}
}

func TestEditSeriesMovingCarriesUNTILAlong(t *testing.T) {
	moved := date(t, "2026-11-02T14:00:00Z")
	series := []string{"DTSTART:20261102T090000Z", "DTEND:20261102T100000Z", "RRULE:FREQ=DAILY;UNTIL=20261104T090000Z", "SUMMARY:Daily"}

	got := editSeries(t, calendarOf(t, series), EventPatch{Start: &moved}, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")

	if want := "Daily@1102T1400 Daily@1103T1400 Daily@1104T1400"; summaries(got) != want {
		t.Errorf("got %s, want %s", summaries(got), want)
	}
}

func TestEditSeriesMoveToAnotherDayWithoutDayRules(t *testing.T) {
	moved := date(t, "2026-11-03T09:00:00Z")
	series := []string{"DTSTART:20261102T090000Z", "DTEND:20261102T100000Z", "RRULE:FREQ=WEEKLY;COUNT=2", "SUMMARY:Weekly"}

	got := editSeries(t, calendarOf(t, series), EventPatch{Start: &moved}, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")

	if want := "Weekly@1103T0900 Weekly@1110T0900"; summaries(got) != want {
		t.Errorf("got %s, want %s", summaries(got), want)
	}
}

func TestEditSeriesRefusals(t *testing.T) {
	newDay := date(t, "2026-11-03T09:00:00Z")
	newHour := date(t, "2026-11-02T14:00:00Z")
	earlier := date(t, "2026-11-02T08:00:00Z")
	byDay := []string{"DTSTART:20261102T090000Z", "DTEND:20261102T100000Z", "RRULE:FREQ=WEEKLY;BYDAY=MO", "SUMMARY:x"}
	byHour := []string{"DTSTART:20261102T090000Z", "DTEND:20261102T100000Z", "RRULE:FREQ=DAILY;BYHOUR=9", "SUMMARY:x"}
	allDay := []string{"DTSTART;VALUE=DATE:20261102", "RRULE:FREQ=WEEKLY;COUNT=2", "SUMMARY:x"}
	timed := []string{"DTSTART:20261102T090000Z", "DTEND:20261102T100000Z", "SUMMARY:x"}
	onlyOverride := []string{"RECURRENCE-ID:20261102T090000Z", "DTSTART:20261102T090000Z", "SUMMARY:x"}

	tests := []struct {
		name  string
		event []string
		patch EventPatch
		want  string
	}{
		{"nothing to change", timed, EventPatch{}, "nothing"},
		{"empty summary", timed, EventPatch{Summary: strPtr(" ")}, "summary"},
		{"moving the day against BYDAY", byDay, EventPatch{Start: &newDay}, "another day"},
		{"moving the hour against BYHOUR", byHour, EventPatch{Start: &newHour}, "another time"},
		{"recurring all-day to timed", allDay, EventPatch{Start: &newHour}, "all-day"},
		{"end before start", timed, EventPatch{End: &earlier}, "end"},
		{"only an override stored", onlyOverride, EventPatch{Summary: strPtr("New")}, "series"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EditSeries([]byte(calendarOf(t, tt.event)), tt.patch, time.UTC, fixedNow)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestEditSingleEventCanChangeShape(t *testing.T) {
	timed := []string{"DTSTART:20261102T090000Z", "DTEND:20261102T100000Z", "SUMMARY:x"}
	allDay := []string{"DTSTART;VALUE=DATE:20261102", "DTEND;VALUE=DATE:20261103", "SUMMARY:x"}
	withDuration := []string{"DTSTART:20261102T090000Z", "DURATION:PT1H", "SUMMARY:x"}
	first, last := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	nine, ten := date(t, "2026-11-10T09:00:00Z"), date(t, "2026-11-10T10:30:00Z")

	t.Run("timed to all-day", func(t *testing.T) {
		got := editSeries(t, calendarOf(t, timed), EventPatch{Start: &first, End: &last, AllDay: true}, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")
		if len(got) != 1 || !got[0].AllDay || !got[0].Start.Equal(first) || !got[0].End.Equal(last) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("all-day to timed", func(t *testing.T) {
		got := editSeries(t, calendarOf(t, allDay), EventPatch{Start: &nine, End: &ten}, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")
		if len(got) != 1 || got[0].AllDay || !got[0].Start.Equal(nine) || !got[0].End.Equal(ten) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a new end replaces a duration", func(t *testing.T) {
		out := string(mustEditSeries(t, calendarOf(t, withDuration), EventPatch{End: &ten}))
		if strings.Contains(out, "DURATION") || !strings.Contains(out, "DTEND:20261110T103000Z") {
			t.Errorf("stored:\n%s", out)
		}
	})
	t.Run("moving keeps a duration", func(t *testing.T) {
		out := string(mustEditSeries(t, calendarOf(t, withDuration), EventPatch{Start: &nine}))
		if !strings.Contains(out, "DURATION:PT1H") || !strings.Contains(out, "DTSTART:20261110T090000Z") || strings.Contains(out, "DTEND") {
			t.Errorf("stored:\n%s", out)
		}
	})
}
