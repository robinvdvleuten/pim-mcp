package ical

import (
	"bytes"
	"fmt"
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
