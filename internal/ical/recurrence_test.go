package ical

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRecurrenceRule(t *testing.T) {
	amsterdam := location(t, "Europe/Amsterdam")
	newYork := location(t, "America/New_York")
	timed := func(zone *time.Location, y int, m time.Month, d, h int) EventFields {
		start := time.Date(y, m, d, h, 0, 0, 0, zone)
		return EventFields{Start: start, End: start.Add(time.Hour), Zone: zone}
	}
	allDay := func(y int, m time.Month, d int) EventFields {
		start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return EventFields{Start: start, End: start.AddDate(0, 0, 1), AllDay: true}
	}
	monday := timed(amsterdam, 2026, 10, 19, 9)

	tests := []struct {
		name    string
		fields  EventFields
		rule    string
		want    string // the stored rule
		wantErr string
	}{
		{name: "weekly on two days", fields: monday, rule: "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=10", want: "FREQ=WEEKLY;COUNT=10;BYDAY=MO,WE"},
		{name: "written back in a fixed order", fields: monday, rule: "BYDAY=MO;INTERVAL=2;FREQ=WEEKLY", want: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO"},
		{name: "lower case", fields: monday, rule: "freq=weekly;byday=mo", want: "FREQ=WEEKLY;BYDAY=MO"},
		{name: "first of the month", fields: timed(amsterdam, 2026, 11, 1, 9), rule: "FREQ=MONTHLY;BYMONTHDAY=1", want: "FREQ=MONTHLY;BYMONTHDAY=1"},
		{name: "last Friday", fields: timed(amsterdam, 2026, 10, 30, 9), rule: "FREQ=MONTHLY;BYDAY=-1FR", want: "FREQ=MONTHLY;BYDAY=-1FR"},
		{name: "week start", fields: monday, rule: "FREQ=WEEKLY;WKST=SU;BYDAY=MO", want: "FREQ=WEEKLY;WKST=SU;BYDAY=MO"},
		{name: "daily without a bound", fields: monday, rule: "FREQ=DAILY", want: "FREQ=DAILY"},
		{name: "yearly", fields: monday, rule: "FREQ=YEARLY", want: "FREQ=YEARLY"},
		{name: "all-day", fields: allDay(2026, 11, 2), rule: "FREQ=WEEKLY;BYDAY=MO", want: "FREQ=WEEKLY;BYDAY=MO"},

		// UNTIL is stored as UTC for a timed event, and as a date for an all-day one.
		{name: "timed, UNTIL as a date, to the end of the day in the event's zone", fields: monday, rule: "FREQ=WEEKLY;UNTIL=20270331", want: "FREQ=WEEKLY;UNTIL=20270331T215959Z"},
		{name: "timed, UNTIL as a date, in a zone behind UTC", fields: timed(newYork, 2026, 10, 19, 9), rule: "FREQ=WEEKLY;UNTIL=20261231", want: "FREQ=WEEKLY;UNTIL=20270101T045959Z"},
		{name: "timed, UNTIL as UTC", fields: monday, rule: "FREQ=WEEKLY;UNTIL=20261231T120000Z", want: "FREQ=WEEKLY;UNTIL=20261231T120000Z"},
		{name: "all-day, UNTIL as a date", fields: allDay(2026, 11, 2), rule: "FREQ=WEEKLY;UNTIL=20261231", want: "FREQ=WEEKLY;UNTIL=20261231"},
		{name: "all-day, UNTIL as UTC", fields: allDay(2026, 11, 2), rule: "FREQ=WEEKLY;UNTIL=20261231T120000Z", want: "FREQ=WEEKLY;UNTIL=20261231"},
		{name: "UNTIL on the start day", fields: monday, rule: "FREQ=DAILY;UNTIL=20261019", want: "FREQ=DAILY;UNTIL=20261019T215959Z"},
		{name: "UNTIL without a zone", fields: monday, rule: "FREQ=WEEKLY;UNTIL=20261231T120000", wantErr: "UNTIL"},
		{name: "UNTIL that is no date", fields: monday, rule: "FREQ=WEEKLY;UNTIL=20261331", wantErr: "20261331"},

		{name: "not a rule", fields: monday, rule: "every week", wantErr: "KEY=VALUE"},
		{name: "empty value", fields: monday, rule: "FREQ=", wantErr: "KEY=VALUE"},
		{name: "unknown frequency", fields: monday, rule: "FREQ=FORTNIGHTLY", wantErr: "FORTNIGHTLY"},
		{name: "no frequency", fields: monday, rule: "BYDAY=MO", wantErr: "FREQ"},
		{name: "unknown part", fields: monday, rule: "FREQ=DAILY;FOO=1", wantErr: "FOO"},
		{name: "SECONDLY", fields: monday, rule: "FREQ=SECONDLY", wantErr: "too often"},
		{name: "MINUTELY", fields: monday, rule: "FREQ=MINUTELY;COUNT=5", wantErr: "too often"},
		{name: "HOURLY", fields: monday, rule: "FREQ=HOURLY", wantErr: "too often"},
		{name: "COUNT and UNTIL", fields: monday, rule: "FREQ=DAILY;COUNT=3;UNTIL=20261231", wantErr: "both COUNT and UNTIL"},
		{name: "COUNT of nothing", fields: monday, rule: "FREQ=DAILY;COUNT=0", wantErr: "COUNT"},
		{name: "INTERVAL of nothing", fields: monday, rule: "FREQ=DAILY;INTERVAL=0", wantErr: "INTERVAL"},
		{name: "a part twice", fields: monday, rule: "FREQ=DAILY;FREQ=WEEKLY", wantErr: "twice"},
		{name: "DTSTART inside", fields: monday, rule: "FREQ=DAILY;DTSTART=20200101", wantErr: "DTSTART"},
		{name: "BYEASTER", fields: monday, rule: "FREQ=YEARLY;BYEASTER=0", wantErr: "BYEASTER"},
		{name: "a value out of bounds", fields: monday, rule: "FREQ=MONTHLY;BYMONTHDAY=40", wantErr: "bymonthday"},
		{name: "with the RRULE prefix", fields: monday, rule: "RRULE:FREQ=DAILY", wantErr: "prefix"},
		{name: "trailing line break", fields: monday, rule: "FREQ=DAILY\n", wantErr: "line breaks"},
		{name: "a property after a line break", fields: monday, rule: "FREQ=DAILY\r\nATTENDEE:mailto:x@example.org", wantErr: "line breaks"},
		{name: "a property after a colon", fields: monday, rule: "FREQ=DAILY:ATTENDEE;CN=x", wantErr: "KEY=VALUE"},
		{name: "a space", fields: monday, rule: "FREQ=DAILY; COUNT=2", wantErr: "KEY=VALUE"},
		{name: "a quote", fields: monday, rule: `FREQ=DAILY;BYDAY="MO"`, wantErr: "KEY=VALUE"},

		{name: "start on another weekday than the rule", fields: timed(amsterdam, 2026, 10, 20, 9), rule: "FREQ=WEEKLY;BYDAY=MO", wantErr: "isn't an occurrence"},
		{name: "start at another hour than the rule", fields: monday, rule: "FREQ=DAILY;BYHOUR=10", wantErr: "isn't an occurrence"},
		{name: "all-day start on another weekday", fields: allDay(2026, 11, 3), rule: "FREQ=WEEKLY;BYDAY=MO", wantErr: "isn't an occurrence"},
		{name: "no date matches", fields: monday, rule: "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30", wantErr: "no occurrence"},
		{name: "ends before it starts", fields: monday, rule: "FREQ=DAILY;UNTIL=20200101", wantErr: "no occurrence"},
		{name: "timed without a zone", fields: EventFields{Start: monday.Start, End: monday.End}, rule: "FREQ=DAILY", wantErr: "time zone"},
		{name: "timed in an unnamed zone", fields: EventFields{Start: monday.Start, End: monday.End, Zone: time.Local}, rule: "FREQ=DAILY", wantErr: "IANA name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := tt.fields
			fields.Recurrence = tt.rule

			got, err := recurrenceRule(fields)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one naming %q (rule %q)", err, tt.wantErr, got)
				}
				if _, err := BuildEvent("uid", fields, fixedNow); err == nil {
					t.Errorf("BuildEvent accepted it")
				}
				if CheckRecurrence(fields) == nil {
					t.Errorf("CheckRecurrence accepted it")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("rule = %q, %v, want %q", got, err, tt.want)
			}
			if CheckRecurrence(fields) != nil {
				t.Errorf("CheckRecurrence refused it")
			}
		})
	}
}

func recurringWeekly(t *testing.T) []byte {
	t.Helper()
	amsterdam := location(t, "Europe/Amsterdam")
	data, err := BuildEvent("uid-1@pim-mcp", EventFields{
		Summary:    "Standup, daily; x",
		Start:      time.Date(2026, 10, 19, 9, 0, 0, 0, amsterdam),
		End:        time.Date(2026, 10, 19, 9, 30, 0, 0, amsterdam),
		Recurrence: "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=20",
		Zone:       amsterdam,
	}, fixedNow)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return data
}

func TestBuildRecurringEventKeepsItsZone(t *testing.T) {
	got := string(recurringWeekly(t))

	for _, want := range []string{
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Amsterdam\r\n",
		"DTSTART;TZID=Europe/Amsterdam:20261019T090000\r\n",
		"DTEND;TZID=Europe/Amsterdam:20261019T093000\r\n",
		"RRULE:FREQ=WEEKLY;COUNT=20;BYDAY=MO,WE\r\n",
		`SUMMARY:Standup\, daily\; x` + "\r\n",
		"UID:uid-1@pim-mcp\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Index(got, "BEGIN:VTIMEZONE") > strings.Index(got, "BEGIN:VEVENT") {
		t.Errorf("the VTIMEZONE comes after the event:\n%s", got)
	}
	if strings.Count(got, "BEGIN:VTIMEZONE") != 1 {
		t.Errorf("want one VTIMEZONE:\n%s", got)
	}
}

func TestBuildRecurringEventKeepsLocalTimeAcrossClockChange(t *testing.T) {
	data := recurringWeekly(t)

	// The clocks go back on 25 October 2026.
	got := expandBytes(t, data, "2026-10-19T00:00:00Z", "2026-11-10T00:00:00Z")

	want := "20261019T070000Z 20261021T070000Z 20261026T080000Z 20261028T080000Z 20261102T080000Z 20261104T080000Z 20261109T080000Z"
	if ids(got) != want {
		t.Fatalf("ids = %s, want %s", ids(got), want)
	}
	amsterdam := location(t, "Europe/Amsterdam")
	for _, o := range got {
		if local := o.Start.In(amsterdam).Format("15:04"); local != "09:00" {
			t.Errorf("%s is at %s local, want 09:00", o.OccurrenceID, local)
		}
		if !o.Recurring || o.Timezone != "Europe/Amsterdam" || o.End.Sub(o.Start) != 30*time.Minute {
			t.Errorf("occurrence = %+v", o)
		}
	}
}

func TestBuildRecurringEventCanBeEditedAndDeletedPerOccurrence(t *testing.T) {
	data := recurringWeekly(t)
	occurrences := expandBytes(t, data, "2026-10-19T00:00:00Z", "2026-11-10T00:00:00Z")
	after := occurrences[2] // the first one after the clocks went back

	edited, err := EditOccurrence(data, after.OccurrenceID, EventPatch{Summary: strPtr("Moved")}, time.UTC, fixedNow)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	got := expandBytes(t, edited, "2026-10-19T00:00:00Z", "2026-11-10T00:00:00Z")
	if len(got) != len(occurrences) || got[2].Summary != "Moved" || got[3].Summary != "Standup, daily; x" || !got[2].Start.Equal(after.Start) {
		t.Errorf("after edit: %s", summaries(got))
	}

	deleted, err := DeleteOccurrence(data, after.OccurrenceID, time.UTC, fixedNow)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	got = expandBytes(t, deleted, "2026-10-19T00:00:00Z", "2026-11-10T00:00:00Z")
	if len(got) != len(occurrences)-1 || ids(got) != strings.Replace(ids(occurrences), after.OccurrenceID+" ", "", 1) {
		t.Errorf("after delete: %s", ids(got))
	}
}

func TestBuildRecurringAllDayEvent(t *testing.T) {
	data, err := BuildEvent("uid-1", EventFields{
		Summary:    "Bins",
		AllDay:     true,
		Start:      time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
		Recurrence: "FREQ=WEEKLY;BYDAY=MO;UNTIL=20261130T120000Z",
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}

	got := string(data)
	for _, want := range []string{"DTSTART;VALUE=DATE:20261102\r\n", "DTEND;VALUE=DATE:20261103\r\n", "RRULE:FREQ=WEEKLY;BYDAY=MO;UNTIL=20261130\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "VTIMEZONE") || strings.Contains(got, "TZID") {
		t.Errorf("an all-day event has no zone:\n%s", got)
	}
	occurrences := expandBytes(t, data, "2026-11-01T00:00:00Z", "2026-12-31T00:00:00Z")
	if len(occurrences) != 5 || !occurrences[0].AllDay || !occurrences[0].Recurring || occurrences[4].OccurrenceID != "20261130" {
		t.Errorf("occurrences = %+v", occurrences)
	}
}

func TestBuildTimedUntilIsUTCAndIncludesTheLastDay(t *testing.T) {
	amsterdam := location(t, "Europe/Amsterdam")
	data, err := BuildEvent("uid-1", EventFields{
		Summary: "x", Start: time.Date(2026, 11, 2, 23, 0, 0, 0, amsterdam), End: time.Date(2026, 11, 3, 0, 0, 0, 0, amsterdam),
		Recurrence: "FREQ=DAILY;UNTIL=20261104", Zone: amsterdam,
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "UNTIL=20261104T225959Z") {
		t.Errorf("stored:\n%s", data)
	}
	if got := expandBytes(t, data, "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"); len(got) != 3 {
		t.Errorf("want the 2nd, 3rd and 4th, got %s", ids(got))
	}
}

// A single event is written as before: in UTC, with no zone and no rule.
func TestBuildSingleEventIsUnchanged(t *testing.T) {
	amsterdam := location(t, "Europe/Amsterdam")
	data, err := BuildEvent("uid-1", EventFields{
		Summary: "Lunch", Start: time.Date(2026, 11, 10, 12, 0, 0, 0, amsterdam), End: time.Date(2026, 11, 10, 13, 0, 0, 0, amsterdam),
		Zone: amsterdam, // Only a recurring event uses it.
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"BEGIN:VCALENDAR", "PRODID:-//pim-mcp//pim-mcp//EN", "VERSION:2.0", "BEGIN:VEVENT",
		"DTEND:20261110T120000Z", "DTSTAMP:20261105T120000Z", "DTSTART:20261110T110000Z", "SUMMARY:Lunch", "UID:uid-1",
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n")
	if string(data) != want {
		t.Errorf("got\n%q\nwant\n%q", data, want)
	}
}

func TestBuildTimezoneMatchesGo(t *testing.T) {
	zones := []string{"Europe/Amsterdam", "America/New_York", "Australia/Sydney", "Asia/Kolkata", "Africa/Casablanca", "Australia/Lord_Howe", "America/St_Johns", "UTC"}
	for _, name := range zones {
		for _, year := range []int{2012, 2026, 2085} {
			t.Run(fmt.Sprintf("%s from %d", name, year), func(t *testing.T) {
				loc := location(t, name)
				component, err := buildTimezone(loc, time.Date(year, 6, 15, 9, 0, 0, 0, loc))
				if err != nil {
					t.Fatal(err)
				}

				// The embedded reader is called directly, so a known name can't stand in for it.
				read, err := embeddedLocation(component)
				if err != nil {
					t.Fatalf("read back: %v", err)
				}
				from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
				until := time.Date(year+timezoneHorizonYears+1, 1, 1, 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
				checked := 0
				for at := from; at.Before(until); at = at.Add(time.Hour) {
					// The definition starts at local midnight on 1 January, which is within
					// a day of the UTC instant used here.
					if at.Before(from.Add(24*time.Hour)) || !at.Before(until.Add(-24*time.Hour)) {
						continue
					}
					_, want := at.In(loc).Zone()
					_, got := at.In(read).Zone()
					if got != want {
						t.Fatalf("at %s the offset is %d, Go says %d", at.Format(time.RFC3339), got, want)
					}
					checked++
				}
				if checked < 24*365*timezoneHorizonYears {
					t.Errorf("checked only %d hours", checked)
				}
			})
		}
	}
}

func TestBuildTimezoneShape(t *testing.T) {
	kolkata := location(t, "Asia/Kolkata")
	component, err := buildTimezone(kolkata, time.Date(2026, 6, 1, 0, 0, 0, 0, kolkata))
	if err != nil {
		t.Fatal(err)
	}
	if len(component.Children) != 1 || component.Children[0].Name != "STANDARD" {
		t.Fatalf("a zone without clock changes wants one STANDARD observance, got %+v", component.Children)
	}
	if got := component.Children[0].Props.Get("TZOFFSETTO").Value; got != "+0530" {
		t.Errorf("offset = %s", got)
	}

	amsterdam := location(t, "Europe/Amsterdam")
	component, err = buildTimezone(amsterdam, time.Date(2026, 6, 1, 0, 0, 0, 0, amsterdam))
	if err != nil {
		t.Fatal(err)
	}
	// One to fix the start, then two a year for 2026 through 2036.
	if len(component.Children) != 1+2*(timezoneHorizonYears+1) {
		t.Errorf("observances = %d", len(component.Children))
	}
	if !usableTimezone(component) {
		t.Errorf("a stored VTIMEZONE must survive repair")
	}
}
