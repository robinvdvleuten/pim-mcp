package ical

//go:generate go run gen_windowszones.go

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Zone data must not depend on the machine: pim-mcp is a single binary.

	goical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

const (
	dateFormat     = "20060102"
	localFormat    = "20060102T150405"
	utcFormat      = "20060102T150405Z"
	maxDurationDay = 100 * 366

	// maxZoneSteps bounds each observance in a VTIMEZONE, which comes from the
	// same strangers as the invite. Outlook starts its rules in 1601, which
	// takes at most 200 yearly steps once moved to 1900.
	maxZoneSteps = 5000
)

var (
	zoneRulesStart = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	zoneRulesEnd   = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// zones turns the date values of one calendar into times.
type zones struct {
	floating   *time.Location
	vtimezones map[string]*goical.Component
	resolved   map[string]*time.Location
}

func newZones(cal *goical.Calendar, floating *time.Location) *zones {
	z := &zones{
		floating:   floating,
		vtimezones: map[string]*goical.Component{},
		resolved:   map[string]*time.Location{},
	}
	for _, child := range cal.Children {
		if child.Name == goical.CompTimezone {
			if prop := child.Props.Get(goical.PropTimezoneID); prop != nil {
				z.vtimezones[prop.Value] = child
			}
		}
	}
	return z
}

// dateTime reads a DATE or DATE-TIME value. The second result reports a DATE,
// which belongs to no zone and is placed at midnight in the user's.
func (z *zones) dateTime(prop *goical.Prop) (time.Time, bool, error) {
	switch {
	case len(prop.Value) == len(dateFormat):
		at, err := time.ParseInLocation(dateFormat, prop.Value, z.floating)
		return at, true, err
	case strings.HasSuffix(prop.Value, "Z"):
		at, err := time.ParseInLocation(utcFormat, prop.Value, time.UTC)
		return at, false, err
	}

	loc := z.floating
	if tzid := prop.Params.Get(goical.PropTimezoneID); tzid != "" {
		var err error
		if loc, err = z.location(tzid); err != nil {
			return time.Time{}, false, err
		}
	}
	at, err := time.ParseInLocation(localFormat, prop.Value, loc)
	if err != nil {
		return time.Time{}, false, err
	}
	return firstInstant(at), false, nil
}

// firstInstant settles a wall clock time that happens twice, when clocks go
// back: RFC 5545 means the first, while Go may pick either.
func firstInstant(at time.Time) time.Time {
	_, offset := at.Zone()
	_, before := at.Add(-24 * time.Hour).Zone()
	if before <= offset {
		return at
	}
	earlier := at.Add(-time.Duration(before-offset) * time.Second)
	if _, earlierOffset := earlier.Zone(); earlierOffset == before {
		return earlier
	}
	return at
}

// location resolves a TZID. Names are preferred over an embedded definition,
// because they stay correct when the sender's definition is out of date.
func (z *zones) location(tzid string) (*time.Location, error) {
	if loc, ok := z.resolved[tzid]; ok {
		return loc, nil
	}
	loc, err := z.findLocation(tzid)
	if err != nil {
		return nil, fmt.Errorf("time zone %q: %w", tzid, err)
	}
	z.resolved[tzid] = loc
	return loc, nil
}

func (z *zones) findLocation(tzid string) (*time.Location, error) {
	if loc := namedLocation(tzid); loc != nil {
		return loc, nil
	}
	if component, ok := z.vtimezones[tzid]; ok {
		return embeddedLocation(component)
	}
	return nil, errors.New("not a known name, and the calendar doesn't define it")
}

func namedLocation(tzid string) *time.Location {
	if name, ok := windowsZones[tzid]; ok {
		tzid = name
	}
	// "Local" would silently mean the zone of whichever machine runs this.
	if tzid == "Local" {
		return nil
	}
	if strings.HasPrefix(tzid, "/") {
		// libical prefixes names, as in /freeassociation.sourceforge.net/Europe/Paris.
		parts := strings.Split(strings.TrimPrefix(tzid, "/"), "/")
		for i := 1; i < len(parts); i++ {
			if loc, err := time.LoadLocation(strings.Join(parts[i:], "/")); err == nil {
				return loc
			}
		}
		return nil
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil
	}
	return loc
}

type zoneTransition struct {
	at     time.Time
	offset int
	isDST  bool
}

// embeddedLocation turns a VTIMEZONE into a real Location, so that recurrence
// keeps working in wall clock time just like with a named zone.
func embeddedLocation(component *goical.Component) (*time.Location, error) {
	var transitions []zoneTransition
	var first time.Time
	var firstOffset int
	for _, child := range component.Children {
		if child.Name != goical.CompTimezoneStandard && child.Name != goical.CompTimezoneDaylight {
			continue
		}
		from, err := utcOffset(child, goical.PropTimezoneOffsetFrom)
		if err != nil {
			return nil, err
		}
		to, err := utcOffset(child, goical.PropTimezoneOffsetTo)
		if err != nil {
			return nil, err
		}
		onsets, err := observanceOnsets(child, from)
		if err != nil {
			return nil, err
		}
		for _, onset := range onsets {
			at := onset.Add(-time.Duration(from) * time.Second)
			if first.IsZero() || at.Before(first) {
				first, firstOffset = at, from
			}
			transitions = append(transitions, zoneTransition{at, to, child.Name == goical.CompTimezoneDaylight})
		}
	}
	if len(transitions) == 0 {
		return nil, errors.New("definition has no observances")
	}
	slices.SortStableFunc(transitions, func(a, b zoneTransition) int { return a.at.Compare(b.at) })

	data, err := encodeTZif(firstOffset, transitions)
	if err != nil {
		return nil, err
	}
	// The empty name marks this as having no IANA name.
	loc, err := time.LoadLocationFromTZData("", data)
	if err != nil {
		return nil, fmt.Errorf("build definition: %w", err)
	}
	return loc, nil
}

var offsetPattern = regexp.MustCompile(`^([+-])(\d{2})(\d{2})(\d{2})?$`)

func utcOffset(component *goical.Component, name string) (int, error) {
	prop := component.Props.Get(name)
	if prop == nil {
		return 0, fmt.Errorf("definition has no %s", name)
	}
	match := offsetPattern.FindStringSubmatch(prop.Value)
	if match == nil {
		return 0, fmt.Errorf("read %s: bad offset", name)
	}
	hours, _ := strconv.Atoi(match[2])
	minutes, _ := strconv.Atoi(match[3])
	seconds, _ := strconv.Atoi(match[4])
	offset := hours*3600 + minutes*60 + seconds
	if match[1] == "-" {
		offset = -offset
	}
	return offset, nil
}

// observanceOnsets returns when an observance starts, as wall clock times
// held in UTC.
func observanceOnsets(component *goical.Component, from int) ([]time.Time, error) {
	prop := component.Props.Get(goical.PropDateTimeStart)
	if prop == nil {
		return nil, errors.New("definition has no DTSTART")
	}
	start, err := time.ParseInLocation(localFormat, prop.Value, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("read definition's DTSTART: %w", err)
	}
	onsets := []time.Time{start}

	for _, prop := range component.Props.Values(goical.PropRecurrenceDates) {
		for value := range strings.SplitSeq(prop.Value, ",") {
			at, err := time.ParseInLocation(localFormat, value, time.UTC)
			if err != nil {
				return nil, fmt.Errorf("read definition's RDATE: %w", err)
			}
			onsets = append(onsets, at)
		}
	}

	prop = component.Props.Get(goical.PropRecurrenceRule)
	if prop == nil {
		return onsets, nil
	}
	option, err := rrule.StrToROptionInLocation(prop.Value, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("read definition's RRULE: %w", err)
	}
	// rrule-go stops without a word once a rule has run for 292 years, and Outlook
	// starts its rules in 1601. The rules repeat yearly, so a later start gives
	// the same transitions.
	option.Dtstart = start
	if start.Before(zoneRulesStart) {
		option.Dtstart = time.Date(zoneRulesStart.Year(), start.Month(), start.Day(), start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
	}
	if !option.Until.IsZero() {
		// UNTIL is true UTC here, while the onsets are wall clock times.
		option.Until = option.Until.Add(time.Duration(from) * time.Second)
	}
	rule, err := rrule.NewRRule(*option)
	if err != nil {
		return nil, fmt.Errorf("read definition's RRULE: %w", err)
	}
	next := rule.Iterator()
	for step := 0; ; step++ {
		if step >= maxZoneSteps {
			return nil, ErrRuleTooExpensive
		}
		at, ok := next()
		if !ok || at.After(zoneRulesEnd) {
			return onsets, nil
		}
		onsets = append(onsets, at)
	}
}

// encodeTZif writes the format time.LoadLocationFromTZData reads (RFC 9636).
// The standard library has no other way to make a Location with transitions.
func encodeTZif(firstOffset int, transitions []zoneTransition) ([]byte, error) {
	type zoneType struct {
		offset int
		isDST  bool
	}
	types := []zoneType{{offset: firstOffset}}
	indexes := make([]byte, len(transitions))
	for i, transition := range transitions {
		zone := zoneType{transition.offset, transition.isDST}
		index := slices.Index(types, zone)
		if index < 0 {
			if len(types) > 255 {
				return nil, errors.New("definition has too many offsets")
			}
			index = len(types)
			types = append(types, zone)
		}
		indexes[i] = byte(index) //nolint:gosec // Limited to 255 just above.
	}

	var out bytes.Buffer
	header := func(transitionCount, typeCount, charCount uint32) {
		out.WriteString("TZif2")
		out.Write(make([]byte, 15))
		for _, count := range []uint32{0, 0, 0, transitionCount, typeCount, charCount} {
			_ = binary.Write(&out, binary.BigEndian, count)
		}
	}
	// Version 2 keeps a block of 32-bit data first, which readers skip. It stays
	// empty because Outlook's transitions start in 1601.
	header(0, 0, 0)
	header(uint32(len(transitions)), uint32(len(types)), 1) //nolint:gosec // Bounded by the caps above.
	for _, transition := range transitions {
		_ = binary.Write(&out, binary.BigEndian, transition.at.Unix())
	}
	out.Write(indexes)
	for _, zone := range types {
		_ = binary.Write(&out, binary.BigEndian, int32(zone.offset)) //nolint:gosec // Limited by the offset pattern.
		isDST := byte(0)
		if zone.isDST {
			isDST = 1
		}
		out.Write([]byte{isDST, 0})
	}
	out.WriteByte(0) // The one empty abbreviation that every type points at.
	return out.Bytes(), nil
}

// LocalZone returns the user's zone with its IANA name where one can be found,
// because time.Local only calls itself "Local". That name can't be asked of Go,
// so it comes from where Go gets the zone itself: TZ, then /etc/localtime.
func LocalZone() *time.Location {
	tz, tzSet := os.LookupEnv("TZ")
	link, _ := os.Readlink("/etc/localtime")
	if name := localZoneName(tz, tzSet, link); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

func localZoneName(tz string, tzSet bool, localtime string) string {
	if tzSet {
		tz = strings.TrimPrefix(tz, ":")
		switch {
		case tz == "":
			return "UTC"
		case strings.HasPrefix(tz, "/"):
			return ""
		}
		return tz
	}
	if _, name, ok := strings.Cut(localtime, "zoneinfo/"); ok {
		return name
	}
	return ""
}

// zoneName returns the name to report: empty when it isn't an IANA name.
func zoneName(loc *time.Location) string {
	if loc == time.Local {
		return ""
	}
	return loc.String()
}

// span is how long an occurrence lasts. Days follow the calendar, so that a
// day stays one day across a clock change, while exact stays exact.
type span struct {
	days  int
	exact time.Duration
}

func (s span) end(start time.Time) time.Time {
	// AddDate goes through the wall clock, which would move a start that falls
	// in the repeated hour of a clock change, so it only runs when needed.
	if s.days != 0 {
		start = firstInstant(start.AddDate(0, 0, s.days))
	}
	return start.Add(s.exact)
}

var durationPattern = regexp.MustCompile(`^([+-])?P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func parseDuration(value string) (span, error) {
	match := durationPattern.FindStringSubmatch(value)
	if match == nil || value == "P" || strings.HasSuffix(value, "T") {
		return span{}, fmt.Errorf("bad duration %q", value)
	}
	if match[1] == "-" {
		return span{}, errors.New("ends before it starts")
	}
	var numbers [5]int
	for i := range numbers {
		if match[i+2] == "" {
			continue
		}
		number, err := strconv.Atoi(match[i+2])
		if err != nil || number > maxDurationDay {
			return span{}, fmt.Errorf("duration %q is too long", value)
		}
		numbers[i] = number
	}
	return span{
		days: numbers[0]*7 + numbers[1],
		exact: time.Duration(numbers[2])*time.Hour +
			time.Duration(numbers[3])*time.Minute +
			time.Duration(numbers[4])*time.Second,
	}, nil
}

// civilDays counts calendar days between two dates, ignoring clock changes.
func civilDays(from, to time.Time) int {
	fromYear, fromMonth, fromDay := from.Date()
	toYear, toMonth, toDay := to.Date()
	a := time.Date(fromYear, fromMonth, fromDay, 0, 0, 0, 0, time.UTC)
	b := time.Date(toYear, toMonth, toDay, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a) / (24 * time.Hour))
}
