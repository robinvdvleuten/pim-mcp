package ical

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

const (
	maxRange       = 366 * 24 * time.Hour
	maxOccurrences = 1000
	// maxRuleSteps bounds the walk from DTSTART to the range. Invites come from
	// strangers, so a rule such as FREQ=SECONDLY must not be free to run long.
	maxRuleSteps = 1_000_000

	occurrenceIDFormat = "20060102T150405Z"
)

// Errors for requests the limits refuse. Results are never silently cut short.
var (
	ErrInvalidRange       = errors.New("range end must be after its start")
	ErrRangeTooLarge      = fmt.Errorf("range is longer than %d days", maxRange/(24*time.Hour))
	ErrTooManyOccurrences = fmt.Errorf("range holds more than %d occurrences, use a shorter range", maxOccurrences)
	ErrRuleTooExpensive   = errors.New("recurrence rule repeats too often to expand")
)

// Expand returns the occurrences that overlap [start, end), ordered by start.
func Expand(cal *goical.Calendar, start, end time.Time) ([]Occurrence, error) {
	if !end.After(start) {
		return nil, ErrInvalidRange
	}
	if end.Sub(start) > maxRange {
		return nil, ErrRangeTooLarge
	}

	occurrences := []Occurrence{}
	for _, s := range groupSeries(cal.Events()) {
		expanded, err := s.expand(start, end)
		if err != nil {
			return nil, fmt.Errorf("event %q: %w", s.uid, err)
		}
		occurrences = append(occurrences, expanded...)
		if len(occurrences) > maxOccurrences {
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

func (s *series) expand(rangeStart, rangeEnd time.Time) ([]Occurrence, error) {
	var occurrences []Occurrence

	// An override replaces its instance wherever it moved to, so whether it is
	// in range depends on its own times, not on the slot it came from.
	overridden := map[int64]bool{}
	for _, event := range s.overrides {
		id, err := recurrenceID(event)
		if err != nil {
			return nil, err
		}
		overridden[id.Unix()] = true

		template, start, duration, err := readEvent(event)
		if err != nil {
			return nil, err
		}
		if template.Status == "cancelled" || !overlaps(start, duration, rangeStart, rangeEnd) {
			continue
		}
		template.Recurring = true
		template.OccurrenceID = occurrenceID(id)
		template.Start, template.End = start, start.Add(duration)
		occurrences = append(occurrences, template)
	}

	for _, event := range s.masters {
		expanded, err := expandMaster(event, overridden, rangeStart, rangeEnd)
		if err != nil {
			return nil, err
		}
		occurrences = append(occurrences, expanded...)
		if len(occurrences) > maxOccurrences {
			return nil, ErrTooManyOccurrences
		}
	}
	return occurrences, nil
}

func expandMaster(event *goical.Event, overridden map[int64]bool, rangeStart, rangeEnd time.Time) ([]Occurrence, error) {
	template, start, duration, err := readEvent(event)
	if err != nil {
		return nil, err
	}
	option, err := event.Props.RecurrenceRule()
	if err != nil {
		return nil, fmt.Errorf("read RRULE: %w", err)
	}
	extra, err := dateList(event, goical.PropRecurrenceDates)
	if err != nil {
		return nil, err
	}
	excluded, err := dateList(event, goical.PropExceptionDates)
	if err != nil {
		return nil, err
	}
	template.Recurring = option != nil || len(extra) > 0

	skip := map[int64]bool{}
	for key := range overridden {
		skip[key] = true
	}
	for _, at := range excluded {
		skip[at.Unix()] = true
	}

	var occurrences []Occurrence
	// add also marks at as done, because RDATE may repeat a rule instance.
	add := func(at time.Time) error {
		if skip[at.Unix()] {
			return nil
		}
		skip[at.Unix()] = true
		if !overlaps(at, duration, rangeStart, rangeEnd) {
			return nil
		}
		occurrence := template
		if template.Recurring {
			occurrence.OccurrenceID = occurrenceID(at)
		}
		occurrence.Start, occurrence.End = at, at.Add(duration)
		occurrences = append(occurrences, occurrence)
		if len(occurrences) > maxOccurrences {
			return ErrTooManyOccurrences
		}
		return nil
	}

	if option == nil {
		// Without a rule, DTSTART is the only instance RDATE doesn't list.
		if err := add(start); err != nil {
			return nil, err
		}
	} else {
		option.Dtstart = start
		rule, err := rrule.NewRRule(*option)
		if err != nil {
			return nil, fmt.Errorf("read RRULE: %w", err)
		}
		next := rule.Iterator()
		for step := 0; ; step++ {
			if step >= maxRuleSteps {
				return nil, ErrRuleTooExpensive
			}
			at, ok := next()
			if !ok || !at.Before(rangeEnd) {
				break
			}
			if err := add(at); err != nil {
				return nil, err
			}
		}
	}

	for _, at := range extra {
		if err := add(at.In(start.Location())); err != nil {
			return nil, err
		}
	}
	return occurrences, nil
}

// readEvent returns an event's shared fields, its start and its duration.
func readEvent(event *goical.Event) (Occurrence, time.Time, time.Duration, error) {
	template := eventTemplate(event)
	if event.Props.Get(goical.PropDateTimeStart) == nil {
		return Occurrence{}, time.Time{}, 0, errors.New("no DTSTART")
	}
	start, err := event.DateTimeStart(nil)
	if err != nil {
		return Occurrence{}, time.Time{}, 0, fmt.Errorf("read DTSTART: %w", err)
	}
	end, err := event.DateTimeEnd(nil)
	if err != nil {
		return Occurrence{}, time.Time{}, 0, fmt.Errorf("read end: %w", err)
	}
	if end.Before(start) {
		return Occurrence{}, time.Time{}, 0, errors.New("ends before it starts")
	}
	template.Timezone = start.Location().String()
	return template, start, end.Sub(start), nil
}

func recurrenceID(event *goical.Event) (time.Time, error) {
	prop := event.Props.Get(goical.PropRecurrenceID)
	// Refused rather than treated as a single override, which would show the
	// later instances unchanged.
	if strings.EqualFold(prop.Params.Get("RANGE"), "THISANDFUTURE") {
		return time.Time{}, errors.New("RECURRENCE-ID with RANGE=THISANDFUTURE is not supported")
	}
	id, err := prop.DateTime(nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("read RECURRENCE-ID: %w", err)
	}
	return id, nil
}

// dateList reads every value of a property that may repeat and may hold a
// comma-separated list, such as EXDATE and RDATE.
func dateList(event *goical.Event, name string) ([]time.Time, error) {
	var dates []time.Time
	for _, prop := range event.Props.Values(name) {
		if prop.ValueType() == goical.ValuePeriod {
			return nil, fmt.Errorf("%s with PERIOD values is not supported", name)
		}
		for value := range strings.SplitSeq(prop.Value, ",") {
			single := prop
			single.Value = value
			at, err := single.DateTime(nil)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			dates = append(dates, at)
		}
	}
	return dates, nil
}

func occurrenceID(at time.Time) string {
	return at.UTC().Format(occurrenceIDFormat)
}

// overlaps treats an event with no duration as a point in time, so it counts
// when it starts inside the range even though it ends where it starts.
func overlaps(start time.Time, duration time.Duration, rangeStart, rangeEnd time.Time) bool {
	if !start.Before(rangeEnd) {
		return false
	}
	if duration == 0 {
		return !start.Before(rangeStart)
	}
	return start.Add(duration).After(rangeStart)
}
