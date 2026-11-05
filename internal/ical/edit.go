package ical

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	repair(cal)
	var out bytes.Buffer
	if err := goical.NewEncoder(&out).Encode(cal); err != nil {
		return nil, fmt.Errorf("encode iCalendar: %w", err)
	}
	return out.Bytes(), nil
}

// atMostOnce lists what go-ical's encoder refuses to write twice, per component.
var atMostOnce = map[string][]string{
	goical.CompCalendar: {goical.PropProductID, goical.PropVersion, goical.PropCalendarScale, goical.PropMethod, goical.PropUID, goical.PropLastModified, goical.PropURL, goical.PropRefreshInterval, goical.PropSource, goical.PropColor},
	goical.CompEvent: {goical.PropDateTimeStamp, goical.PropUID, goical.PropDateTimeStart, goical.PropClass, goical.PropCreated, goical.PropDescription, goical.PropGeo, goical.PropLastModified,
		goical.PropLocation, goical.PropOrganizer, goical.PropPriority, goical.PropRecurrenceRule, goical.PropSequence, goical.PropStatus, goical.PropSummary, goical.PropTransparency, goical.PropURL,
		goical.PropRecurrenceID, goical.PropDateTimeEnd, goical.PropDuration, goical.PropColor},
}

// repair makes a calendar one that go-ical's encoder will write. Its rules follow
// RFC 5545, but real invites break them, and a stricter writer than reader would
// make mail we can read impossible to store.
func repair(cal *goical.Calendar) {
	if cal.Props.Get(goical.PropVersion) == nil {
		cal.Props.SetText(goical.PropVersion, "2.0")
	}
	if cal.Props.Get(goical.PropProductID) == nil {
		cal.Props.SetText(goical.PropProductID, productID)
	}
	keepFirst(cal.Component)
	cal.Children = slices.DeleteFunc(cal.Children, func(child *goical.Component) bool {
		return child.Name == goical.CompTimezone && !usableTimezone(child)
	})
	for _, child := range cal.Children {
		if child.Name != goical.CompEvent {
			continue
		}
		keepFirst(child)
		if child.Props.Get(goical.PropDateTimeStamp) == nil {
			child.Props.SetDateTime(goical.PropDateTimeStamp, time.Now().UTC())
		}
		if child.Props.Get(goical.PropDateTimeEnd) != nil {
			child.Props.Del(goical.PropDuration)
		}
		child.Children = slices.DeleteFunc(child.Children, func(nested *goical.Component) bool {
			return nested.Name != goical.CompAlarm
		})
	}
}

func keepFirst(component *goical.Component) {
	for _, name := range atMostOnce[component.Name] {
		if props := component.Props[name]; len(props) > 1 {
			component.Props[name] = props[:1]
		}
	}
}

// usableTimezone says whether a VTIMEZONE defines anything. One that doesn't can
// only be dropped: an event naming it still resolves if the name is a known one.
func usableTimezone(component *goical.Component) bool {
	if len(component.Props[goical.PropTimezoneID]) != 1 || len(component.Children) == 0 {
		return false
	}
	for _, child := range component.Children {
		if child.Name != goical.CompTimezoneStandard && child.Name != goical.CompTimezoneDaylight {
			return false
		}
		for _, name := range []string{goical.PropDateTimeStart, goical.PropTimezoneOffsetTo, goical.PropTimezoneOffsetFrom} {
			if len(child.Props[name]) != 1 {
				return false
			}
		}
	}
	return true
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

	newEnd, err := patchedEnd(z, patch, newStart, length)
	if err != nil {
		return err
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

func patchedEnd(z *zones, patch EventPatch, newStart time.Time, length span) (time.Time, error) {
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
		return time.Time{}, errors.New("the end has to be after the start")
	}
	return newEnd, nil
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
	component.Props.Set(momentProp(name, reference, at, isDate))
}

func momentProp(name string, reference *goical.Prop, at time.Time, isDate bool) *goical.Prop {
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
	return prop
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

// ErrNoSuchOccurrence means an event doesn't have, or no longer has, an occurrence.
var ErrNoSuchOccurrence = errors.New("this event has no such occurrence")

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

// occurrenceSlot reads an occurrence_id. It reports whether it names a date.
func occurrenceSlot(z *zones, id string) (time.Time, bool, error) {
	switch len(id) {
	case len(dateFormat):
		if at, err := time.ParseInLocation(dateFormat, id, z.floating); err == nil {
			return at, true, nil
		}
	case len(utcFormat):
		if at, err := time.ParseInLocation(utcFormat, id, time.UTC); err == nil {
			return at, false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("%q is not an occurrence_id from calendar_list_events", id)
}

// findOccurrence locates an instance: its override if it has one, and whether
// the series itself produces it.
func findOccurrence(z *zones, s *series, slot time.Time) (*goical.Event, bool, error) {
	var override *goical.Event
	for _, event := range s.overrides {
		id, _, err := recurrenceID(z, event)
		if err != nil {
			return nil, false, err
		}
		if id.Equal(slot) {
			override = event
		}
	}
	if len(s.masters) == 0 {
		return override, false, nil
	}
	occurrences, err := expandMaster(z, s.masters[0], nil, slot, slot.Add(time.Second))
	if err != nil {
		return nil, false, err
	}
	for _, occurrence := range occurrences {
		if occurrence.Recurring && occurrence.Start.Equal(slot) {
			return override, true, nil
		}
	}
	return override, false, nil
}

func loadSeries(data []byte, id string, loc *time.Location) (*goical.Calendar, *zones, *series, time.Time, *goical.Event, bool, error) {
	fail := func(err error) (*goical.Calendar, *zones, *series, time.Time, *goical.Event, bool, error) {
		return nil, nil, nil, time.Time{}, nil, false, err
	}
	cal, err := Parse(data)
	if err != nil {
		return fail(err)
	}
	z := newZones(cal, loc)
	all := groupSeries(cal.Events())
	if len(all) != 1 {
		return fail(errors.New("expected one event, found several"))
	}
	s := all[0]
	if len(s.masters) == 1 && len(s.overrides) == 0 &&
		s.masters[0].Props.Get(goical.PropRecurrenceRule) == nil && s.masters[0].Props.Get(goical.PropRecurrenceDates) == nil {
		return fail(errors.New("this event doesn't repeat, use scope series"))
	}
	slot, _, err := occurrenceSlot(z, id)
	if err != nil {
		return fail(err)
	}
	override, inSeries, err := findOccurrence(z, s, slot)
	if err != nil {
		return fail(err)
	}
	if override == nil && !inSeries {
		return fail(fmt.Errorf("%w: %s", ErrNoSuchOccurrence, id))
	}
	return cal, z, s, slot, override, inSeries, nil
}

// EditOccurrence changes one instance of a recurring event, by way of an override
// that says how it differs from the rest. The override is a copy of the series,
// so it keeps the alarms and everything else the instance had.
func EditOccurrence(data []byte, id string, patch EventPatch, loc *time.Location, now time.Time) ([]byte, error) {
	if err := patch.check(); err != nil {
		return nil, err
	}
	cal, z, s, slot, override, _, err := loadSeries(data, id, loc)
	if err != nil {
		return nil, err
	}
	if override == nil {
		if override, err = newOverride(z, cal, s.masters[0], slot); err != nil {
			return nil, err
		}
	}
	applyText(override.Component, patch)
	if patch.Start != nil || patch.End != nil {
		if err := moveOverride(z, override, patch); err != nil {
			return nil, err
		}
	}
	touch(override.Component, now)
	return encode(cal)
}

func moveOverride(z *zones, event *goical.Event, patch EventPatch) error {
	startProp := event.Props.Get(goical.PropDateTimeStart)
	if startProp == nil {
		return errors.New("no DTSTART")
	}
	oldStart, isDate, err := z.dateTime(startProp)
	if err != nil {
		return fmt.Errorf("read DTSTART: %w", err)
	}
	if patch.AllDay != isDate && patch.Start != nil {
		return errors.New("an occurrence can't become all-day or timed on its own")
	}
	length, err := readLength(z, event, oldStart, isDate)
	if err != nil {
		return err
	}
	newStart := oldStart
	if patch.Start != nil {
		if patch.AllDay {
			newStart = midnightIn(*patch.Start, z.floating)
		} else {
			newStart = patch.Start.In(oldStart.Location())
		}
	}
	newEnd, err := patchedEnd(z, patch, newStart, length)
	if err != nil {
		return err
	}
	setMoment(event.Component, goical.PropDateTimeStart, startProp, newStart, isDate)
	return setEnd(event.Component, patch, startProp, newEnd, isDate)
}

// newOverride copies the series for one slot and adds it to the calendar.
func newOverride(z *zones, cal *goical.Calendar, master *goical.Event, slot time.Time) (*goical.Event, error) {
	startProp := master.Props.Get(goical.PropDateTimeStart)
	start, isDate, err := z.dateTime(startProp)
	if err != nil {
		return nil, fmt.Errorf("read DTSTART: %w", err)
	}
	length, err := readLength(z, master, start, isDate)
	if err != nil {
		return nil, err
	}
	copied := cloneComponent(master.Component)
	for _, name := range []string{goical.PropRecurrenceRule, goical.PropRecurrenceDates, goical.PropExceptionDates, "EXRULE"} {
		copied.Props.Del(name)
	}
	at := slot.In(start.Location())
	setMoment(copied, goical.PropRecurrenceID, startProp, at, isDate)
	setMoment(copied, goical.PropDateTimeStart, startProp, at, isDate)
	if endProp := copied.Props.Get(goical.PropDateTimeEnd); endProp != nil {
		setMoment(copied, goical.PropDateTimeEnd, endProp, length.end(at), isDate)
	}
	cal.Children = append(cal.Children, copied)
	return &goical.Event{Component: copied}, nil
}

func cloneComponent(component *goical.Component) *goical.Component {
	clone := goical.NewComponent(component.Name)
	for name, props := range component.Props {
		for _, prop := range props {
			copied := prop
			copied.Params = make(goical.Params, len(prop.Params))
			for key, values := range prop.Params {
				copied.Params[key] = slices.Clone(values)
			}
			clone.Props[name] = append(clone.Props[name], copied)
		}
	}
	for _, child := range component.Children {
		clone.Children = append(clone.Children, cloneComponent(child))
	}
	return clone
}

// DeleteOccurrence removes one instance of a recurring event. It returns nil if
// that leaves nothing to store. An EXDATE removes it from the series, and any
// override goes with it, as it would otherwise bring the instance back.
func DeleteOccurrence(data []byte, id string, loc *time.Location, now time.Time) ([]byte, error) {
	cal, z, s, slot, override, inSeries, err := loadSeries(data, id, loc)
	if err != nil {
		return nil, err
	}
	if override != nil {
		cal.Children = slices.DeleteFunc(cal.Children, func(child *goical.Component) bool {
			return child == override.Component
		})
	}
	if len(s.masters) == 0 {
		if len(s.overrides) == 1 {
			return nil, nil
		}
		return encode(cal)
	}
	master := s.masters[0]
	if inSeries || override != nil {
		startProp := master.Props.Get(goical.PropDateTimeStart)
		start, isDate, err := z.dateTime(startProp)
		if err != nil {
			return nil, fmt.Errorf("read DTSTART: %w", err)
		}
		master.Props.Add(momentProp(goical.PropExceptionDates, startProp, slot.In(start.Location()), isDate))
	}
	touch(master.Component, now)
	return encode(cal)
}
