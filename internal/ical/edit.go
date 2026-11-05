package ical

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
)

const productID = "-//pim-mcp//pim-mcp//EN"

// EventFields is what a person says about an event. An all-day event's Start
// and End are dates, in any zone, with End being the day after the last day.
type EventFields struct {
	Summary     string
	Location    string
	Description string
	Start       time.Time
	End         time.Time
	AllDay      bool
}

// BuildEvent encodes a single event. Timed events are stored in UTC, because
// naming a zone would oblige them to carry its full definition, and without a
// recurrence rule the instant is all that matters.
func BuildEvent(uid string, fields EventFields, now time.Time) ([]byte, error) {
	event := goical.NewEvent()
	event.Props.SetText(goical.PropUID, uid)
	event.Props.SetDateTime(goical.PropDateTimeStamp, now.UTC())
	setText(event.Component, goical.PropSummary, fields.Summary)
	setText(event.Component, goical.PropLocation, fields.Location)
	setText(event.Component, goical.PropDescription, fields.Description)
	if fields.AllDay {
		event.Props.SetDate(goical.PropDateTimeStart, fields.Start)
		event.Props.SetDate(goical.PropDateTimeEnd, fields.End)
	} else {
		event.Props.SetDateTime(goical.PropDateTimeStart, fields.Start.UTC())
		event.Props.SetDateTime(goical.PropDateTimeEnd, fields.End.UTC())
	}

	cal := goical.NewCalendar()
	cal.Props.SetText(goical.PropVersion, "2.0")
	cal.Props.SetText(goical.PropProductID, productID)
	cal.Children = append(cal.Children, event.Component)
	return encode(cal)
}

func encode(cal *goical.Calendar) ([]byte, error) {
	var out bytes.Buffer
	if err := goical.NewEncoder(&out).Encode(cal); err != nil {
		return nil, fmt.Errorf("encode iCalendar: %w", err)
	}
	return out.Bytes(), nil
}

// setText stores text escaped. A carriage return isn't escaped by go-ical, and
// would otherwise start a new line that a reader takes for a property.
func setText(component *goical.Component, name, text string) {
	if text == "" {
		component.Props.Del(name)
		return
	}
	component.Props.SetText(name, strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text))
}

// EventPatch says what to change about an event. A nil field stays as it is. An
// empty Location or Description clears it. Start and End are dates when AllDay
// is set, with End being the day after the last day.
type EventPatch struct {
	Summary     *string
	Location    *string
	Description *string
	Start       *time.Time
	End         *time.Time
	AllDay      bool
}

func (p EventPatch) isEmpty() bool {
	return p.Summary == nil && p.Location == nil && p.Description == nil && p.Start == nil && p.End == nil
}

func (p EventPatch) check() error {
	if p.isEmpty() {
		return errors.New("nothing to change")
	}
	if p.Summary != nil && strings.TrimSpace(*p.Summary) == "" {
		return errors.New("an event needs a summary")
	}
	return nil
}

// EditSeries applies a patch to the master of a stored event, and so to every
// occurrence that hasn't been changed on its own. Everything it isn't asked to
// change stays, including what it doesn't understand. Floating values are read
// in loc.
func EditSeries(data []byte, patch EventPatch, loc *time.Location, now time.Time) ([]byte, error) {
	if err := patch.check(); err != nil {
		return nil, err
	}
	cal, err := Parse(data)
	if err != nil {
		return nil, err
	}
	series := groupSeries(cal.Events())
	if len(series) != 1 || len(series[0].masters) != 1 {
		return nil, errors.New("this event isn't a whole series, only changes to single occurrences of one are stored here")
	}
	z := newZones(cal, loc)
	master := series[0].masters[0]

	applyText(master.Component, patch)
	if patch.Start != nil || patch.End != nil {
		if err := moveSeries(z, series[0], patch); err != nil {
			return nil, err
		}
	}
	touch(master.Component, now)
	return encode(cal)
}

func applyText(component *goical.Component, patch EventPatch) {
	for name, value := range map[string]*string{
		goical.PropSummary:     patch.Summary,
		goical.PropLocation:    patch.Location,
		goical.PropDescription: patch.Description,
	} {
		if value != nil {
			setText(component, name, *value)
		}
	}
}

// touch records that the event changed, which other clients go by (RFC 5545).
func touch(component *goical.Component, now time.Time) {
	sequence := 0
	if prop := component.Props.Get(goical.PropSequence); prop != nil {
		sequence, _ = strconv.Atoi(prop.Value)
	}
	prop := goical.NewProp(goical.PropSequence)
	prop.Value = strconv.Itoa(sequence + 1)
	component.Props.Set(prop)
	component.Props.SetDateTime(goical.PropDateTimeStamp, now.UTC())
	component.Props.SetDateTime(goical.PropLastModified, now.UTC())
}

// moveSeries changes when a series happens. Exceptions name their instance by
// its original time, so they have to move with it or they'd stop applying.
func moveSeries(z *zones, s *series, patch EventPatch) error {
	master := s.masters[0]
	startProp := master.Props.Get(goical.PropDateTimeStart)
	if startProp == nil {
		return errors.New("no DTSTART")
	}
	oldStart, oldIsDate, err := z.dateTime(startProp)
	if err != nil {
		return fmt.Errorf("read DTSTART: %w", err)
	}
	length, err := readLength(z, master, oldStart, oldIsDate)
	if err != nil {
		return err
	}
	rule := master.Props.Get(goical.PropRecurrenceRule)
	recurring := rule != nil || master.Props.Get(goical.PropRecurrenceDates) != nil
	if recurring && oldIsDate != patch.AllDay && patch.Start != nil {
		return errors.New("a recurring all-day event can't become a timed one, or the other way round")
	}

	newStart := oldStart
	if patch.Start != nil {
		if patch.AllDay {
			newStart = midnightIn(*patch.Start, z.floating)
		} else {
			newStart = patch.Start.In(oldStart.Location())
		}
	}
	delta := wallClock(newStart).Sub(wallClock(oldStart))
	if recurring && rule != nil {
		if err := checkRuleAllows(rule.Value, delta, oldStart, newStart); err != nil {
			return err
		}
	}

	var newEnd time.Time
	switch {
	case patch.End != nil && patch.AllDay:
		newEnd = midnightIn(*patch.End, z.floating)
	case patch.End != nil:
		newEnd = *patch.End
	default:
		newEnd = length.end(newStart)
	}
	if !newEnd.After(newStart) {
		return errors.New("the end has to be after the start")
	}

	if err := shiftExceptions(z, s, delta); err != nil {
		return err
	}
	if rule != nil && delta != 0 {
		rule.Value = shiftUntil(rule.Value, delta)
	}

	setMoment(master.Component, goical.PropDateTimeStart, startProp, newStart, patch.AllDay && patch.Start != nil || oldIsDate && patch.Start == nil)
	return setEnd(master.Component, patch, startProp, newEnd, oldIsDate)
}

// setEnd keeps DURATION for an event that only moved, as its length is unchanged.
func setEnd(component *goical.Component, patch EventPatch, startProp *goical.Prop, end time.Time, wasDate bool) error {
	if patch.End == nil && component.Props.Get(goical.PropDuration) != nil {
		return nil
	}
	if patch.End == nil && component.Props.Get(goical.PropDateTimeEnd) == nil && patch.AllDay == wasDate {
		return nil
	}
	component.Props.Del(goical.PropDuration)
	reference := component.Props.Get(goical.PropDateTimeEnd)
	if reference == nil {
		reference = startProp
	}
	setMoment(component, goical.PropDateTimeEnd, reference, end, patch.AllDay || wasDate && patch.Start == nil)
	return nil
}

// setMoment writes a time the way the property it replaces was written: in the
// same zone, so a series keeps following that zone's clock.
func setMoment(component *goical.Component, name string, reference *goical.Prop, at time.Time, isDate bool) {
	prop := goical.NewProp(name)
	switch {
	case isDate:
		prop.SetValueType(goical.ValueDate)
		prop.Value = at.Format(dateFormat)
	case reference != nil && reference.Params.Get(goical.PropTimezoneID) != "":
		prop.Params.Set(goical.PropTimezoneID, reference.Params.Get(goical.PropTimezoneID))
		prop.Value = at.Format(localFormat)
	case reference != nil && len(reference.Value) == len(localFormat):
		prop.Value = at.Format(localFormat)
	default:
		prop.Value = at.UTC().Format(utcFormat)
	}
	component.Props.Set(prop)
}

// wallClock returns what a clock in the time's own zone shows, as a UTC time, so
// that the difference between two of them ignores clock changes.
func wallClock(at time.Time) time.Time {
	year, month, day := at.Date()
	hour, minute, second := at.Clock()
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC)
}

func midnightIn(day time.Time, loc *time.Location) time.Time {
	year, month, date := day.Date()
	return time.Date(year, month, date, 0, 0, 0, 0, loc)
}

// checkRuleAllows refuses a move that a rule pinned to certain days or hours
// would contradict, since working out the rule that was meant is a guess.
func checkRuleAllows(rule string, delta time.Duration, oldStart, newStart time.Time) error {
	upper := strings.ToUpper(rule)
	if delta == 0 {
		return nil
	}
	if wallClock(oldStart).Format(dateFormat) != wallClock(newStart).Format(dateFormat) {
		for _, part := range []string{"BYDAY", "BYMONTHDAY", "BYMONTH", "BYYEARDAY", "BYWEEKNO", "BYSETPOS"} {
			if strings.Contains(upper, part) {
				return fmt.Errorf("this series repeats by %s, so it can't move to another day: create a new event instead", part)
			}
		}
	}
	if wallClock(oldStart).Format("150405") != wallClock(newStart).Format("150405") {
		for _, part := range []string{"BYHOUR", "BYMINUTE", "BYSECOND"} {
			if strings.Contains(upper, part) {
				return fmt.Errorf("this series repeats by %s, so it can't move to another time: create a new event instead", part)
			}
		}
	}
	return nil
}

var untilValuePattern = regexp.MustCompile(`(?i)(UNTIL=)(\d{8})(T\d{6}Z?)?`)

func shiftUntil(rule string, delta time.Duration) string {
	return untilValuePattern.ReplaceAllStringFunc(rule, func(match string) string {
		value := match[len("UNTIL="):]
		layout := dateFormat
		switch {
		case strings.HasSuffix(value, "Z"):
			layout = utcFormat
		case len(value) == len(localFormat):
			layout = localFormat
		}
		at, err := time.Parse(layout, value)
		if err != nil {
			return match
		}
		return match[:len("UNTIL=")] + at.Add(delta).Format(layout)
	})
}

// shiftExceptions moves the instances that overrides and date lists name. An
// override still in its original slot moves too, while one that was moved
// elsewhere stays there.
func shiftExceptions(z *zones, s *series, delta time.Duration) error {
	if delta == 0 {
		return nil
	}
	master := s.masters[0]
	for _, name := range []string{goical.PropExceptionDates, goical.PropRecurrenceDates} {
		for i := range master.Props[name] {
			if err := shiftProp(z, &master.Props[name][i], delta); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			}
		}
	}
	for _, override := range s.overrides {
		idProp := override.Props.Get(goical.PropRecurrenceID)
		id, _, err := z.dateTime(idProp)
		if err != nil {
			return fmt.Errorf("read RECURRENCE-ID: %w", err)
		}
		startProp := override.Props.Get(goical.PropDateTimeStart)
		var inPlace bool
		if startProp != nil {
			start, _, err := z.dateTime(startProp)
			if err != nil {
				return fmt.Errorf("read DTSTART: %w", err)
			}
			inPlace = start.Equal(id)
		}
		if err := shiftProp(z, idProp, delta); err != nil {
			return fmt.Errorf("read RECURRENCE-ID: %w", err)
		}
		if !inPlace {
			continue
		}
		if err := shiftProp(z, startProp, delta); err != nil {
			return fmt.Errorf("read DTSTART: %w", err)
		}
		if endProp := override.Props.Get(goical.PropDateTimeEnd); endProp != nil {
			if err := shiftProp(z, endProp, delta); err != nil {
				return fmt.Errorf("read DTEND: %w", err)
			}
		}
	}
	return nil
}

// shiftProp moves each value of a property by a wall clock difference, leaving
// how it's written alone.
func shiftProp(z *zones, prop *goical.Prop, delta time.Duration) error {
	values := strings.Split(prop.Value, ",")
	for i, value := range values {
		single := *prop
		single.Value = value
		at, isDate, err := z.dateTime(&single)
		if err != nil {
			return err
		}
		shifted := wallClock(at).Add(delta)
		switch {
		case isDate:
			values[i] = shifted.Format(dateFormat)
		case strings.HasSuffix(value, "Z"):
			values[i] = at.Add(delta).UTC().Format(utcFormat)
		default:
			values[i] = shifted.Format(localFormat)
		}
	}
	prop.Value = strings.Join(values, ",")
	return nil
}
