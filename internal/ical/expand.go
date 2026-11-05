package ical

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

const (
	maxRange = 366 * 24 * time.Hour
	// MaxOccurrences is the most a single query may return.
	MaxOccurrences = 1000
	// maxRuleSteps bounds the walk from DTSTART to the range. Invites come from
	// strangers, so a rule such as FREQ=SECONDLY must not be free to run long.
	maxRuleSteps = 1_000_000
	// maxRuleAge stays under the 292 years after which rrule-go silently stops
	// producing occurrences.
	maxRuleAge = 290 * 365 * 24 * time.Hour
)

// Errors for requests the limits refuse. Results are never silently cut short.
var (
	ErrInvalidRange       = errors.New("range end must be after its start")
	ErrRangeTooLarge      = fmt.Errorf("range is longer than %d days", maxRange/(24*time.Hour))
	ErrTooManyOccurrences = fmt.Errorf("range holds more than %d occurrences, use a shorter range", MaxOccurrences)
	ErrRuleTooExpensive   = errors.New("recurrence rule repeats too often to expand")
	ErrRuleTooOld         = errors.New("recurrence rule started too long ago to expand")
)

// untilDatePattern finds an UNTIL holding a date rather than a date-time.
var untilDatePattern = regexp.MustCompile(`(?i)UNTIL=\d{8}(;|$)`)

// CheckRange refuses a range that Expand would refuse, so that callers can do so
// before fetching anything.
func CheckRange(start, end time.Time) error {
	if !end.After(start) {
		return ErrInvalidRange
	}
	if end.Sub(start) > maxRange {
		return ErrRangeTooLarge
	}
	return nil
}

// Expand returns the occurrences that overlap [start, end), ordered by start.
// Values without a zone of their own, such as all-day events, are read in loc:
// the zone of the user who asked.
func Expand(cal *goical.Calendar, start, end time.Time, loc *time.Location) ([]Occurrence, error) {
	if err := CheckRange(start, end); err != nil {
		return nil, err
	}

	z := newZones(cal, loc)
	occurrences := []Occurrence{}
	for _, s := range groupSeries(cal.Events()) {
		expanded, err := s.expand(z, start, end)
		if err != nil {
			return nil, fmt.Errorf("event %q: %w", s.uid, err)
		}
		occurrences = append(occurrences, expanded...)
		if len(occurrences) > MaxOccurrences {
			return nil, ErrTooManyOccurrences
		}
	}

	slices.SortStableFunc(occurrences, func(a, b Occurrence) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return strings.Compare(a.UID, b.UID)
	})
	return occurrences, nil
}

// FirstOccurrence returns the earliest occurrence of the event stored in data, or
// nil if it has none. Looking widens from a day to a year, because a tight range
// can't hit the cap on occurrences while a year can.
func FirstOccurrence(data []byte, loc *time.Location) (*Occurrence, error) {
	cal, err := Parse(data)
	if err != nil {
		return nil, err
	}
	z := newZones(cal, loc)
	for _, s := range groupSeries(cal.Events()) {
		for _, event := range append(slices.Clone(s.masters), s.overrides...) {
			_, start, _, err := readEvent(z, event)
			if err != nil {
				return nil, err
			}
			for _, days := range []int{1, 366} {
				occurrences, err := Expand(cal, start, start.AddDate(0, 0, days), loc)
				if err != nil {
					return nil, err
				}
				if len(occurrences) > 0 {
					return &occurrences[0], nil
				}
			}
		}
	}
	return nil, nil
}

// series holds the VEVENTs that share a UID: normally one master with its
// rule, plus an override per changed instance. Invites to a single instance
// arrive as overrides with no master.
type series struct {
	uid       string
	masters   []*goical.Event
	overrides []*goical.Event
}

func groupSeries(events []goical.Event) []*series {
	var ordered []*series
	byUID := map[string]*series{}
	for i := range events {
		event := &events[i]
		uid := text(event, goical.PropUID)
		s, ok := byUID[uid]
		if !ok {
			s = &series{uid: uid}
			byUID[uid] = s
			ordered = append(ordered, s)
		}
		if event.Props.Get(goical.PropRecurrenceID) != nil {
			s.overrides = append(s.overrides, event)
		} else {
			s.masters = append(s.masters, event)
		}
	}
	return ordered
}

func (s *series) expand(z *zones, rangeStart, rangeEnd time.Time) ([]Occurrence, error) {
	var occurrences []Occurrence

	// An override replaces its instance wherever it moved to, so whether it is
	// in range depends on its own times, not on the slot it came from.
	overridden := map[int64]bool{}
	for _, event := range s.overrides {
		id, idIsDate, err := recurrenceID(z, event)
		if err != nil {
			return nil, err
		}
		overridden[id.Unix()] = true

		template, start, length, err := readEvent(z, event)
		if err != nil {
			return nil, err
		}
		if template.Status == "cancelled" || !overlaps(start, length.end(start), rangeStart, rangeEnd) {
			continue
		}
		template.Recurring = true
		template.OccurrenceID = occurrenceID(id, idIsDate)
		template.Start, template.End = start, length.end(start)
		occurrences = append(occurrences, template)
	}

	for _, event := range s.masters {
		expanded, err := expandMaster(z, event, overridden, rangeStart, rangeEnd)
		if err != nil {
			return nil, err
		}
		occurrences = append(occurrences, expanded...)
		if len(occurrences) > MaxOccurrences {
			return nil, ErrTooManyOccurrences
		}
	}
	return occurrences, nil
}

func expandMaster(z *zones, event *goical.Event, overridden map[int64]bool, rangeStart, rangeEnd time.Time) ([]Occurrence, error) {
	template, start, length, err := readEvent(z, event)
	if err != nil {
		return nil, err
	}
	rule, err := readRule(event, start, template.AllDay)
	if err != nil {
		return nil, err
	}
	extra, err := dateList(z, event, goical.PropRecurrenceDates)
	if err != nil {
		return nil, err
	}
	excluded, err := dateList(z, event, goical.PropExceptionDates)
	if err != nil {
		return nil, err
	}
	template.Recurring = rule != nil || len(extra) > 0

	skip := map[int64]bool{}
	for key := range overridden {
		skip[key] = true
	}
	// Some clients exclude a day from a timed series with a date, which RFC 5545
	// doesn't allow but whose meaning is clear.
	skipDays := map[string]bool{}
	for _, value := range excluded {
		if value.isDate && !template.AllDay {
			skipDays[value.at.Format(dateFormat)] = true
		} else {
			skip[value.at.Unix()] = true
		}
	}

	var occurrences []Occurrence
	// add also marks at as done, because RDATE may repeat a rule instance.
	add := func(at time.Time) error {
		if skip[at.Unix()] || skipDays[at.In(z.floating).Format(dateFormat)] {
			return nil
		}
		skip[at.Unix()] = true
		end := length.end(at)
		if !overlaps(at, end, rangeStart, rangeEnd) {
			return nil
		}
		occurrence := template
		if template.Recurring {
			occurrence.OccurrenceID = occurrenceID(at, template.AllDay)
		}
		occurrence.Start, occurrence.End = at, end
		occurrences = append(occurrences, occurrence)
		if len(occurrences) > MaxOccurrences {
			return ErrTooManyOccurrences
		}
		return nil
	}

	if rule != nil && rangeEnd.Sub(start) > maxRuleAge {
		return nil, ErrRuleTooOld
	}

	if rule == nil {
		// Without a rule, DTSTART is the only instance RDATE doesn't list.
		if err := add(start); err != nil {
			return nil, err
		}
	} else {
		next := rule.Iterator()
		for step := 0; ; step++ {
			if step >= maxRuleSteps {
				return nil, ErrRuleTooExpensive
			}
			at, ok := next()
			if !ok || !at.Before(rangeEnd) {
				break
			}
			if err := add(firstInstant(at)); err != nil {
				return nil, err
			}
		}
	}

	for _, value := range extra {
		if err := add(value.at.In(start.Location())); err != nil {
			return nil, err
		}
	}
	return occurrences, nil
}

// readEvent returns an event's shared fields, its start and its length.
func readEvent(z *zones, event *goical.Event) (Occurrence, time.Time, span, error) {
	template := eventTemplate(event)
	prop := event.Props.Get(goical.PropDateTimeStart)
	if prop == nil {
		return Occurrence{}, time.Time{}, span{}, errors.New("no DTSTART")
	}
	start, isDate, err := z.dateTime(prop)
	if err != nil {
		return Occurrence{}, time.Time{}, span{}, fmt.Errorf("read DTSTART: %w", err)
	}
	length, err := readLength(z, event, start, isDate)
	if err != nil {
		return Occurrence{}, time.Time{}, span{}, err
	}
	template.AllDay = isDate
	template.Timezone = zoneName(start.Location())
	return template, start, length, nil
}

// readLength follows RFC 5545 3.8.5.3: DTEND gives every occurrence the same
// exact length, while the days of a DURATION follow the calendar.
func readLength(z *zones, event *goical.Event, start time.Time, isDate bool) (span, error) {
	if prop := event.Props.Get(goical.PropDateTimeEnd); prop != nil {
		end, _, err := z.dateTime(prop)
		if err != nil {
			return span{}, fmt.Errorf("read DTEND: %w", err)
		}
		switch {
		case end.Before(start):
			return span{}, errors.New("ends before it starts")
		case isDate && end.Equal(start):
			// Not allowed, but it can only mean the one day.
			return span{days: 1}, nil
		case isDate:
			return span{days: civilDays(start, end)}, nil
		}
		return span{exact: end.Sub(start)}, nil
	}
	if prop := event.Props.Get(goical.PropDuration); prop != nil {
		length, err := parseDuration(prop.Value)
		if err != nil {
			return span{}, fmt.Errorf("read DURATION: %w", err)
		}
		return length, nil
	}
	if isDate {
		return span{days: 1}, nil
	}
	return span{}, nil
}

// readRule returns the event's RRULE, or nil if it has none.
func readRule(event *goical.Event, start time.Time, allDay bool) (*rrule.RRule, error) {
	prop := event.Props.Get(goical.PropRecurrenceRule)
	if prop == nil {
		return nil, nil
	}
	option, err := rrule.StrToROptionInLocation(prop.Value, start.Location())
	if err != nil {
		return nil, fmt.Errorf("read RRULE: %w", err)
	}
	// An UNTIL that is a date, or that belongs to an all-day event, includes that
	// date. Read as a moment, it could fall before the last day's midnight.
	if !option.Until.IsZero() && (allDay || untilDatePattern.MatchString(prop.Value)) {
		year, month, day := option.Until.Date()
		option.Until = time.Date(year, month, day, 23, 59, 59, 0, start.Location())
	}
	option.Dtstart = start
	rule, err := rrule.NewRRule(*option)
	if err != nil {
		return nil, fmt.Errorf("read RRULE: %w", err)
	}
	return rule, nil
}

func recurrenceID(z *zones, event *goical.Event) (time.Time, bool, error) {
	prop := event.Props.Get(goical.PropRecurrenceID)
	// Refused rather than treated as a single override, which would show the
	// later instances unchanged.
	if strings.EqualFold(prop.Params.Get("RANGE"), "THISANDFUTURE") {
		return time.Time{}, false, errors.New("RECURRENCE-ID with RANGE=THISANDFUTURE is not supported")
	}
	id, isDate, err := z.dateTime(prop)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read RECURRENCE-ID: %w", err)
	}
	return id, isDate, nil
}

type dateValue struct {
	at     time.Time
	isDate bool
}

// dateList reads every value of a property that may repeat and may hold a
// comma-separated list, such as EXDATE and RDATE.
func dateList(z *zones, event *goical.Event, name string) ([]dateValue, error) {
	var dates []dateValue
	for _, prop := range event.Props.Values(name) {
		if prop.ValueType() == goical.ValuePeriod {
			return nil, fmt.Errorf("%s with PERIOD values is not supported", name)
		}
		for value := range strings.SplitSeq(prop.Value, ",") {
			single := prop
			single.Value = value
			at, isDate, err := z.dateTime(&single)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			dates = append(dates, dateValue{at, isDate})
		}
	}
	return dates, nil
}

// occurrenceID names an instance the way RECURRENCE-ID would: a date for
// all-day events, since that is the same date in every zone.
func occurrenceID(at time.Time, isDate bool) string {
	if isDate {
		return at.Format(dateFormat)
	}
	return at.UTC().Format(utcFormat)
}

// overlaps treats an event with no length as a point in time, so it counts when
// it starts inside the range even though it ends where it starts.
func overlaps(start, end, rangeStart, rangeEnd time.Time) bool {
	if !start.Before(rangeEnd) {
		return false
	}
	if end.Equal(start) {
		return !start.Before(rangeStart)
	}
	return end.After(rangeStart)
}
