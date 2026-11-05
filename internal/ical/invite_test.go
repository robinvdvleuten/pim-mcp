package ical

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func inviteOf(method string, components ...string) []byte {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN"}
	if method != "" {
		lines = append(lines, "METHOD:"+method)
	}
	lines = append(lines, components...)
	lines = append(lines, "END:VCALENDAR", "")
	return []byte(strings.Join(lines, "\r\n"))
}

func veventLines(uid string, lines ...string) string {
	return strings.Join(append(append([]string{"BEGIN:VEVENT", "UID:" + uid, "DTSTAMP:20261001T120000Z"}, lines...), "END:VEVENT"), "\r\n")
}

func TestParseInvite(t *testing.T) {
	data := inviteOf("REQUEST", veventLines("one", "SEQUENCE:3", "DTSTART:20261110T090000Z", "DTEND:20261110T100000Z", "SUMMARY:Kickoff", "ORGANIZER;CN=Jan:mailto:jan@example.org"))

	got, err := ParseInvite(data, time.UTC)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if got.Method != MethodRequest || len(got.Events) != 1 {
		t.Fatalf("got %+v", got)
	}
	e := got.Events[0]
	if e.UID != "one" || e.Sequence != 3 || e.First == nil || e.First.Summary != "Kickoff" || e.First.Organizer != "jan@example.org" {
		t.Errorf("event = %+v, first = %+v", e, e.First)
	}
}

func TestParseInviteMethods(t *testing.T) {
	event := veventLines("one", "DTSTART:20261110T090000Z", "SUMMARY:x")

	tests := []struct {
		method  string
		want    string
		wantErr error
	}{
		{"", MethodPublish, nil},
		{"PUBLISH", MethodPublish, nil},
		{"publish", MethodPublish, nil},
		{"REQUEST", MethodRequest, nil},
		{"CANCEL", MethodCancel, nil},
		{"REPLY", "", ErrUnsupportedMethod},
		{"COUNTER", "", ErrUnsupportedMethod},
		{"REFRESH", "", ErrUnsupportedMethod},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, err := ParseInvite(inviteOf(tt.method, event), time.UTC)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Method != tt.want {
				t.Errorf("method = %q, want %q", got.Method, tt.want)
			}
		})
	}
}

func TestParseInviteGroupsByUID(t *testing.T) {
	data := inviteOf("PUBLISH",
		veventLines("a", "SEQUENCE:1", "DTSTART:20261110T090000Z", "RRULE:FREQ=DAILY;COUNT=2", "SUMMARY:A"),
		veventLines("b", "DTSTART:20261111T090000Z", "SUMMARY:B"),
		veventLines("a", "SEQUENCE:2", "RECURRENCE-ID:20261111T090000Z", "DTSTART:20261111T120000Z", "SUMMARY:A moved"))

	got, err := ParseInvite(data, time.UTC)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(got.Events) != 2 {
		t.Fatalf("events = %+v", got.Events)
	}
	a, b := got.Events[0], got.Events[1]
	if a.UID != "a" || a.Sequence != 2 || !a.First.Recurring {
		t.Errorf("a = %+v, want its highest sequence", a)
	}
	if b.UID != "b" || b.First.Summary != "B" {
		t.Errorf("b = %+v", b)
	}
}

func TestParseInviteRefusals(t *testing.T) {
	tests := map[string][]byte{
		"not iCalendar": []byte("hello"),
		"no events":     inviteOf("PUBLISH"),
		"no UID":        inviteOf("PUBLISH", "BEGIN:VEVENT\r\nDTSTAMP:20261001T120000Z\r\nDTSTART:20261110T090000Z\r\nEND:VEVENT"),
		"no start":      inviteOf("PUBLISH", veventLines("one", "SUMMARY:x")),
		"unknown zone":  inviteOf("PUBLISH", veventLines("one", "DTSTART;TZID=Mars/Olympus:20261110T090000")),
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInvite(data, time.UTC); err == nil {
				t.Error("parse passed")
			}
		})
	}
}

func TestStoredSequence(t *testing.T) {
	data := inviteOf("", veventLines("a", "SEQUENCE:1", "DTSTART:20261110T090000Z"), veventLines("a", "SEQUENCE:4", "RECURRENCE-ID:20261111T090000Z", "DTSTART:20261111T090000Z"))

	got, err := StoredSequence(data)
	if err != nil {
		t.Fatalf("sequence: %v", err)
	}

	if got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

func storable(t *testing.T, data []byte, uid string) string {
	t.Helper()
	invite, err := ParseInvite(data, time.UTC)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := invite.Storable(uid)
	if err != nil {
		t.Fatalf("storable: %v", err)
	}
	return string(out)
}

func TestStorableDropsMethod(t *testing.T) {
	out := storable(t, inviteOf("REQUEST", veventLines("one", "DTSTART:20261110T090000Z", "SUMMARY:x")), "one")

	if strings.Contains(out, "METHOD") {
		t.Errorf("stored:\n%s", out)
	}
	for _, want := range []string{"VERSION:2.0", "PRODID:", "UID:one", "SUMMARY:x"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
}

func TestStorableOnlyHoldsItsOwnEvent(t *testing.T) {
	out := storable(t, inviteOf("PUBLISH", veventLines("one", "DTSTART:20261110T090000Z"), veventLines("two", "DTSTART:20261111T090000Z")), "one")

	if strings.Contains(out, "UID:two") || !strings.Contains(out, "UID:one") {
		t.Errorf("stored:\n%s", out)
	}
}

func TestStorableUnknownUID(t *testing.T) {
	invite, err := ParseInvite(inviteOf("PUBLISH", veventLines("one", "DTSTART:20261110T090000Z")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := invite.Storable("two"); err == nil {
		t.Error("storable passed")
	}
}

func TestStorableEscapesText(t *testing.T) {
	// Radicale cuts text at a bare comma when it stores it.
	out := storable(t, inviteOf("PUBLISH", veventLines("one", "DTSTART:20261110T090000Z",
		"SUMMARY:Lunch, then coffee",
		`LOCATION:Already\, escaped`,
		`DESCRIPTION:a;b\nc`,
		"CATEGORIES:WORK,HOME",
		"ATTENDEE;CN=\"Jan, the first\":mailto:jan@example.org")), "one")

	for _, want := range []string{`SUMMARY:Lunch\, then coffee`, `LOCATION:Already\, escaped`, `DESCRIPTION:a\;b\nc`, "CATEGORIES:WORK,HOME"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Jan, the first") {
		t.Errorf("parameter lost:\n%s", out)
	}
}

func TestStorableKeepsTheInvitesDetails(t *testing.T) {
	out := storable(t, inviteOf("REQUEST", veventLines("one", "DTSTART:20261110T090000Z", "SEQUENCE:3", "ORGANIZER;CN=Jan:mailto:jan@example.org", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:me@example.org", "X-MICROSOFT-CDO-BUSYSTATUS:BUSY",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT15M", "DESCRIPTION:Soon", "END:VALARM")), "one")

	for _, want := range []string{"SEQUENCE:3", "ORGANIZER;CN=Jan:mailto:jan@example.org", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:me@example.org", "X-MICROSOFT-CDO-BUSYSTATUS:BUSY", "BEGIN:VALARM", "TRIGGER:-PT15M"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
}

func TestStorableMapsWindowsZones(t *testing.T) {
	vtimezone := strings.Join([]string{"BEGIN:VTIMEZONE", "TZID:W. Europe Standard Time", "BEGIN:STANDARD", "DTSTART:16010101T030000", "TZOFFSETFROM:+0200", "TZOFFSETTO:+0100", "RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10", "END:STANDARD", "BEGIN:DAYLIGHT", "DTSTART:16010101T020000", "TZOFFSETFROM:+0100", "TZOFFSETTO:+0200", "RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=3", "END:DAYLIGHT", "END:VTIMEZONE"}, "\r\n")
	data := inviteOf("REQUEST", vtimezone, veventLines("one",
		"DTSTART;TZID=W. Europe Standard Time:20261110T090000", "DTEND;TZID=W. Europe Standard Time:20261110T100000",
		"RRULE:FREQ=DAILY;COUNT=3", "EXDATE;TZID=W. Europe Standard Time:20261111T090000"),
		veventLines("one", "RECURRENCE-ID;TZID=W. Europe Standard Time:20261112T090000", "DTSTART;TZID=W. Europe Standard Time:20261112T120000", "SUMMARY:Moved"))

	out := storable(t, data, "one")

	if strings.Contains(out, "W. Europe") {
		t.Errorf("a Windows name is left:\n%s", out)
	}
	for _, want := range []string{"DTSTART;TZID=Europe/Berlin:20261110T090000", "EXDATE;TZID=Europe/Berlin:20261111T090000", "RECURRENCE-ID;TZID=Europe/Berlin:20261112T090000", "TZID:Europe/Berlin"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
	cal, err := Parse([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := Expand(cal, date(t, "2026-11-01T00:00:00Z"), date(t, "2026-12-01T00:00:00Z"), time.UTC)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(got) != 2 || got[0].Timezone != "Europe/Berlin" || got[1].Summary != "Moved" {
		t.Errorf("got %+v", got)
	}
}

func TestStorableKeepsEmbeddedZonesItCantName(t *testing.T) {
	name := "(UTC+01:00) Amsterdam, Berlin"
	vtimezone := strings.Join([]string{"BEGIN:VTIMEZONE", "TZID:" + name, "BEGIN:STANDARD", "DTSTART:19700101T000000", "TZOFFSETFROM:+0100", "TZOFFSETTO:+0100", "END:STANDARD", "END:VTIMEZONE"}, "\r\n")

	out := storable(t, inviteOf("REQUEST", vtimezone, veventLines("one", `DTSTART;TZID="`+name+`":20261110T090000`)), "one")

	if !strings.Contains(out, "BEGIN:VTIMEZONE") || !strings.Contains(out, name) {
		t.Errorf("stored:\n%s", out)
	}
}

func TestMergeReplacesAWholeEvent(t *testing.T) {
	stored := inviteOf("", veventLines("one", "DTSTART:20261110T090000Z", "SUMMARY:Old"))
	invite, err := ParseInvite(inviteOf("REQUEST", veventLines("one", "SEQUENCE:1", "DTSTART:20261110T100000Z", "SUMMARY:New")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	out, err := invite.MergeInto(stored, "one")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if strings.Contains(string(out), "SUMMARY:Old") || !strings.Contains(string(out), "SUMMARY:New") {
		t.Errorf("stored:\n%s", out)
	}
}

func TestMergeOfASingleOccurrenceKeepsTheSeries(t *testing.T) {
	// Organizers send a change to one occurrence as just that occurrence. Storing it
	// as is would lose the whole series.
	stored := inviteOf("",
		veventLines("one", "DTSTART:20261109T090000Z", "RRULE:FREQ=DAILY;COUNT=3", "SUMMARY:Series"),
		veventLines("one", "RECURRENCE-ID:20261110T090000Z", "DTSTART:20261110T120000Z", "SUMMARY:Old override"))
	invite, err := ParseInvite(inviteOf("REQUEST", veventLines("one", "SEQUENCE:2", "RECURRENCE-ID:20261110T090000Z", "DTSTART:20261110T150000Z", "SUMMARY:New override")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	out, err := invite.MergeInto(stored, "one")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	got := expandBytes(t, out, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")
	if want := "Series@1109T0900 Series@1111T0900 New override@1110T1500"; summaries(sortedByID(got)) != want {
		t.Errorf("got %s, want %s", summaries(sortedByID(got)), want)
	}
	if strings.Contains(string(out), "Old override") {
		t.Errorf("stored:\n%s", out)
	}
}

// sortedByID orders masters' occurrences first, to compare without caring where a
// moved occurrence lands.
func sortedByID(occurrences []Occurrence) []Occurrence {
	var plain, others []Occurrence
	for _, o := range occurrences {
		if o.Summary == "Series" {
			plain = append(plain, o)
		} else {
			others = append(others, o)
		}
	}
	return append(plain, others...)
}
