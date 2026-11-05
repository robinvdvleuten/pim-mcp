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
	for _, event := range cal.Events() {
		expanded, err := expandEvent(&event, start, end)
		if err != nil {
			uid, _ := event.Props.Text(goical.PropUID)
			return nil, fmt.Errorf("event %q: %w", uid, err)
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

func expandEvent(event *goical.Event, rangeStart, rangeEnd time.Time) ([]Occurrence, error) {
	template, err := eventTemplate(event)
	if err != nil {
		return nil, err
	}
	if event.Props.Get(goical.PropDateTimeStart) == nil {
		return nil, errors.New("no DTSTART")
	}
	start, err := event.DateTimeStart(nil)
	if err != nil {
		return nil, fmt.Errorf("read DTSTART: %w", err)
	}
	end, err := event.DateTimeEnd(nil)
	if err != nil {
		return nil, fmt.Errorf("read end: %w", err)
	}
	if end.Before(start) {
		return nil, errors.New("ends before it starts")
	}
	duration := end.Sub(start)
	template.Timezone = start.Location().String()

	option, err := event.Props.RecurrenceRule()
	if err != nil {
		return nil, fmt.Errorf("read RRULE: %w", err)
	}
	if option == nil {
		if !overlaps(start, duration, rangeStart, rangeEnd) {
			return nil, nil
		}
		template.Start, template.End = start, end
		return []Occurrence{template}, nil
	}

	template.Recurring = true
	option.Dtstart = start
	rule, err := rrule.NewRRule(*option)
	if err != nil {
		return nil, fmt.Errorf("read RRULE: %w", err)
	}

	var occurrences []Occurrence
	next := rule.Iterator()
	for step := 0; ; step++ {
		if step >= maxRuleSteps {
			return nil, ErrRuleTooExpensive
		}
		at, ok := next()
		if !ok || !at.Before(rangeEnd) {
			return occurrences, nil
		}
		if !overlaps(at, duration, rangeStart, rangeEnd) {
			continue
		}
		occurrence := template
		occurrence.OccurrenceID = at.UTC().Format(occurrenceIDFormat)
		occurrence.Start, occurrence.End = at, at.Add(duration)
		occurrences = append(occurrences, occurrence)
		if len(occurrences) > maxOccurrences {
			return nil, ErrTooManyOccurrences
		}
	}
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
