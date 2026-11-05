package calendar

import (
	"slices"
	"testing"
	"time"
)

func at(hour, minute int) time.Time {
	return time.Date(2026, 11, 10, hour, minute, 0, 0, time.UTC)
}

func TestMergeBusy(t *testing.T) {
	tests := []struct {
		name string
		in   []Interval
		want []Interval
	}{
		{"nothing", nil, []Interval{}},
		{"apart stay apart", []Interval{{at(9, 0), at(10, 0)}, {at(11, 0), at(12, 0)}}, []Interval{{at(9, 0), at(10, 0)}, {at(11, 0), at(12, 0)}}},
		{"overlapping merge", []Interval{{at(9, 0), at(10, 30)}, {at(10, 0), at(11, 0)}}, []Interval{{at(9, 0), at(11, 0)}}},
		{"touching merge", []Interval{{at(9, 0), at(10, 0)}, {at(10, 0), at(11, 0)}}, []Interval{{at(9, 0), at(11, 0)}}},
		{"contained disappears", []Interval{{at(9, 0), at(12, 0)}, {at(10, 0), at(11, 0)}, {at(13, 0), at(14, 0)}}, []Interval{{at(9, 0), at(12, 0)}, {at(13, 0), at(14, 0)}}},
		{"order doesn't matter", []Interval{{at(13, 0), at(14, 0)}, {at(9, 0), at(10, 0)}}, []Interval{{at(9, 0), at(10, 0)}, {at(13, 0), at(14, 0)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeBusy(tt.in); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFreeBusy(t *testing.T) {
	client, _ := reportServer(t, map[string]string{
		"/a/": eventResponse("/a/1.ics", "1", icsEvent("meeting", "20261110T090000Z", "DTEND:20261110T100000Z")) +
			eventResponse("/a/2.ics", "2", icsEvent("tentative", "20261110T093000Z", "DTEND:20261110T103000Z", "STATUS:TENTATIVE")) +
			eventResponse("/a/3.ics", "3", icsEvent("free-time", "20261110T120000Z", "DTEND:20261110T130000Z", "TRANSP:TRANSPARENT")) +
			eventResponse("/a/4.ics", "4", icsEvent("cancelled", "20261110T140000Z", "DTEND:20261110T150000Z", "STATUS:CANCELLED")) +
			eventResponse("/a/5.ics", "5", icsEvent("reminder", "20261110T160000Z")) +
			eventResponse("/a/6.ics", "6", "broken"),
		"/b/": eventResponse("/b/1.ics", "7", icsEvent("other", "20261110T170000Z", "DTEND:20261110T180000Z", "TRANSP:OPAQUE")) +
			eventResponse("/b/2.ics", "8", icsEvent("past-the-end", "20261110T233000Z", "DTEND:20261111T003000Z")) +
			eventResponse("/b/3.ics", "9", icsEvent("before-the-start", "20261109T233000Z", "DTEND:20261110T003000Z")),
	})

	got, err := client.FreeBusy(t.Context(), []string{"/a/", "/b/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("free busy: %v", err)
	}

	want := []Interval{
		{at(0, 0), at(0, 30)}, // cut at the start of the range
		{at(9, 0), at(10, 30)},
		{at(17, 0), at(18, 0)},
		{at(23, 30), rangeEnd}, // cut at the end of the range
	}
	if !slices.Equal(got.Busy, want) {
		t.Errorf("busy = %v, want %v", got.Busy, want)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].EventID != "/a/6.ics" {
		t.Errorf("skipped = %+v, want the broken event named, as the answer may be wrong without it", got.Skipped)
	}
}

func TestFreeBusyAllDayEventsCount(t *testing.T) {
	client, _ := reportServer(t, map[string]string{
		"/a/": eventResponse("/a/1.ics", "1", icsEvent("holiday", "20261110")),
	})

	got, err := client.FreeBusy(t.Context(), []string{"/a/"}, rangeStart, rangeEnd, time.UTC)
	if err != nil {
		t.Fatalf("free busy: %v", err)
	}

	if !slices.Equal(got.Busy, []Interval{{rangeStart, rangeEnd}}) {
		t.Errorf("busy = %v", got.Busy)
	}
}
