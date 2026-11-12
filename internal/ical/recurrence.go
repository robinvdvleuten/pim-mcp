package ical

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// recurrenceWindow bounds the search for a rule's first occurrence. A rule that
// has none in that time (such as the 30th of February) is as good as empty, and
// the bound keeps a hopeless rule from walking to the year 9999.
const recurrenceWindow = 8 // years

// ruleShape is all that may be written into the calendar: KEY=VALUE pairs. It has
// no room for a colon, space or line break, so a rule can't add a property.
var ruleShape = regexp.MustCompile(`^[A-Z][A-Z-]*=[A-Z0-9,+-]+(;[A-Z][A-Z-]*=[A-Z0-9,+-]+)*$`)

// CheckRecurrence refuses the recurrence of fields if BuildEvent would. It lets
// callers fail before they connect to anything.
func CheckRecurrence(fields EventFields) error {
	if fields.Recurrence == "" {
		return nil
	}
	_, err := recurrenceRule(fields)
	return err
}

// recurrenceRule validates the agent's RRULE against the event's start and
// returns it in the form to store. Start is in the event's zone for a timed
// event, and its date is what counts for an all-day one.
func recurrenceRule(fields EventFields) (string, error) {
	rule := strings.ToUpper(fields.Recurrence)
	if strings.HasPrefix(rule, "RRULE:") {
		return "", errors.New("recurrence is the rule alone, such as FREQ=WEEKLY;BYDAY=MO, without the RRULE: prefix")
	}
	if !ruleShape.MatchString(rule) {
		return "", errors.New("recurrence must be KEY=VALUE pairs separated by semicolons, such as FREQ=WEEKLY;BYDAY=MO, with no spaces or line breaks")
	}

	parts := map[string]string{}
	for pair := range strings.SplitSeq(rule, ";") {
		key, value, _ := strings.Cut(pair, "=")
		if _, dup := parts[key]; dup {
			return "", fmt.Errorf("recurrence has %s twice", key)
		}
		parts[key] = value
	}
	// DTSTART is the event's own, and BYEASTER is an extension other clients
	// don't read.
	for _, key := range []string{"DTSTART", "BYEASTER"} {
		if _, ok := parts[key]; ok {
			return "", fmt.Errorf("recurrence can't use %s", key)
		}
	}
	if _, ok := parts["COUNT"]; ok {
		if _, ok := parts["UNTIL"]; ok {
			return "", errors.New("recurrence has both COUNT and UNTIL, and RFC 5545 allows only one of them")
		}
	}

	option, err := rrule.StrToROptionInLocation(rule, time.UTC)
	if err != nil {
		return "", fmt.Errorf("recurrence isn't a valid rule: %w", err)
	}
	if option.Freq > rrule.DAILY {
		return "", fmt.Errorf("recurrence FREQ=%s repeats too often, the shortest allowed is DAILY", option.Freq)
	}
	// rrule-go reads these as "unset", which would quietly change the rule.
	if value, ok := parts["COUNT"]; ok && option.Count < 1 {
		return "", fmt.Errorf("recurrence COUNT=%s must be 1 or more", value)
	}
	if value, ok := parts["INTERVAL"]; ok && option.Interval < 1 {
		return "", fmt.Errorf("recurrence INTERVAL=%s must be 1 or more", value)
	}

	until, hasUntil, err := untilFor(parts["UNTIL"], fields)
	if err != nil {
		return "", err
	}
	option.Until = time.Time{}
	if hasUntil {
		option.Until = until
	}

	// The rule is written back from the parsed options, so what is stored is what
	// was checked. rrule-go writes UNTIL as a date-time, which an all-day event
	// can't have, so that one is added by hand.
	stored := option.RRuleString()
	if hasUntil && fields.AllDay {
		option.Until = time.Time{}
		stored = option.RRuleString() + ";UNTIL=" + until.Format(dateFormat)
		option.Until = until
	}

	if err := checkFirstOccurrence(*option, fields); err != nil {
		return "", err
	}
	return stored, nil
}

// untilFor turns the agent's UNTIL into the form the event needs: a UTC
// date-time for a timed event, and a date for an all-day one. A date given for a
// timed event means through the end of that day in the event's zone.
func untilFor(value string, fields EventFields) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	switch {
	case len(value) == len(dateFormat):
		day, err := time.Parse(dateFormat, value)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("recurrence UNTIL=%s isn't a date such as 20261231", value)
		}
		if fields.AllDay {
			return day, true, nil
		}
		zone, err := recurrenceZone(fields)
		if err != nil {
			return time.Time{}, false, err
		}
		return time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, zone).UTC(), true, nil
	case len(value) == len(utcFormat) && strings.HasSuffix(value, "Z"):
		at, err := time.Parse(utcFormat, value)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("recurrence UNTIL=%s isn't a date-time such as 20261231T235959Z", value)
		}
		if fields.AllDay {
			// An all-day event has no zone, so the date as written is the one.
			year, month, day := at.Date()
			return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), true, nil
		}
		return at, true, nil
	}
	return time.Time{}, false, fmt.Errorf("recurrence UNTIL=%s must be a date such as 20261231, or a UTC date-time ending in Z", value)
}

// recurrenceZone returns the zone a timed recurring event is kept in.
func recurrenceZone(fields EventFields) (*time.Location, error) {
	zone := fields.Zone
	if zone == nil {
		return nil, errors.New("a recurring timed event needs a time zone, so that it keeps its local time when clocks change")
	}
	name := zone.String()
	if name == "" || name == "Local" || !zoneName.MatchString(name) {
		return nil, fmt.Errorf("time zone %q has no IANA name, and a recurring event must name its zone", name)
	}
	return zone, nil
}

var zoneName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+/-]*$`)

// checkFirstOccurrence holds the rule to RFC 5545's advice that DTSTART is the
// rule's first instance. Without that, the first occurrence listed would not be
// the start the agent asked for.
func checkFirstOccurrence(option rrule.ROption, fields EventFields) error {
	start := fields.Start
	if fields.AllDay {
		year, month, day := start.Date()
		start = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	} else {
		zone, err := recurrenceZone(fields)
		if err != nil {
			return err
		}
		start = start.In(zone)
	}
	option.Dtstart = start
	if option.Count == 0 && option.Until.IsZero() {
		option.Until = start.AddDate(recurrenceWindow, 0, 0)
	}
	rule, err := rrule.NewRRule(option)
	if err != nil {
		return fmt.Errorf("recurrence isn't a valid rule: %w", err)
	}
	first, ok, err := instances(rule)()
	switch {
	case err != nil:
		return fmt.Errorf("recurrence isn't a valid rule: %w", err)
	case !ok:
		return errors.New("recurrence has no occurrence: no date matches the rule on or after the start, within its UNTIL if it has one")
	case !first.Equal(start):
		return fmt.Errorf("the start %s isn't an occurrence of the recurrence, whose first is %s: move the start to a day the rule picks, or change the rule",
			describe(start, fields.AllDay), describe(first, fields.AllDay))
	}
	return nil
}

func describe(at time.Time, allDay bool) string {
	if allDay {
		return at.Format(time.DateOnly + " (Mon)")
	}
	return at.Format("2006-01-02 15:04 (Mon)")
}
