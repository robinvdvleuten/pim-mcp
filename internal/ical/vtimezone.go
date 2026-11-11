package ical

import (
	"fmt"
	"time"

	goical "github.com/emersion/go-ical"
)

// timezoneHorizonYears is how far past the event's first year a generated
// VTIMEZONE lists clock changes. After that the last offset is the one a reader
// that only has this definition will use. This project's own reader goes by the
// IANA name first, so it stays exact.
const timezoneHorizonYears = 10

// maxTimezoneSteps stops the walk if a zone were ever to loop.
const maxTimezoneSteps = 10000

type zoneState struct {
	name   string
	offset int
	dst    bool
}

func stateAt(at time.Time) zoneState {
	name, offset := at.Zone()
	return zoneState{name, offset, at.IsDST()}
}

// buildTimezone writes the VTIMEZONE for loc that RFC 5545 asks for beside any
// TZID. It lists the zone's real clock changes from 1 January of from's year up
// to the horizon, one observance each, so no recurrence rule has to be inferred.
// The first observance only fixes the offset in force at the start.
func buildTimezone(loc *time.Location, from time.Time) (*goical.Component, error) {
	first := time.Date(from.Year(), time.January, 1, 0, 0, 0, 0, loc)
	end := time.Date(from.Year()+timezoneHorizonYears+1, time.January, 1, 0, 0, 0, 0, loc)

	vtz := goical.NewComponent(goical.CompTimezone)
	vtz.Props.SetText(goical.PropTimezoneID, loc.String())

	previous := stateAt(first)
	vtz.Children = append(vtz.Children, observance(first, previous.offset))
	at := first
	for range maxTimezoneSteps {
		_, next := at.ZoneBounds()
		switch {
		case next.IsZero() || !next.Before(end):
			return vtz, nil
		case !next.After(at):
			// Past the last year in the zone's data, Go answers from a rule, and on the
			// last day of a leap year its bound lies before the time asked about.
			// Moving on an hour gets out of that day.
			at = at.Add(time.Hour)
		default:
			// The same rule answers with the end of the year when nothing changes then.
			if state := stateAt(next); state != previous {
				vtz.Children = append(vtz.Children, observance(next, previous.offset))
				previous = state
			}
			at = next
		}
	}
	return nil, fmt.Errorf("time zone %s has too many clock changes", loc)
}

// observance describes the zone from the instant at, which must be the start of
// a period. Its DTSTART is in the offset before it, as RFC 5545 says.
func observance(at time.Time, offsetFrom int) *goical.Component {
	name, offsetTo := at.Zone()
	kind := goical.CompTimezoneStandard
	if at.IsDST() {
		kind = goical.CompTimezoneDaylight
	}
	component := goical.NewComponent(kind)
	start := goical.NewProp(goical.PropDateTimeStart)
	start.Value = at.In(time.FixedZone("", offsetFrom)).Format(localFormat)
	component.Props.Set(start)
	component.Props.Set(offsetProp(goical.PropTimezoneOffsetFrom, offsetFrom))
	component.Props.Set(offsetProp(goical.PropTimezoneOffsetTo, offsetTo))
	component.Props.SetText(goical.PropTimezoneName, name)
	return component
}

func offsetProp(name string, seconds int) *goical.Prop {
	sign := '+'
	if seconds < 0 {
		sign, seconds = '-', -seconds
	}
	prop := goical.NewProp(name)
	prop.Value = fmt.Sprintf("%c%02d%02d", sign, seconds/3600, seconds%3600/60)
	if seconds%60 != 0 {
		prop.Value += fmt.Sprintf("%02d", seconds%60)
	}
	return prop
}
