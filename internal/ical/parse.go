// Package ical turns iCalendar data into plain occurrences, so nothing above
// it has to deal with recurrence rules or the iCalendar format. It does no I/O.
package ical

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
)

// Occurrence is one concrete instance of an event.
type Occurrence struct {
	UID string `json:"uid"`
	// OccurrenceID identifies an instance of a recurring event. It is empty for
	// events that don't repeat.
	OccurrenceID string    `json:"occurrence_id"`
	Summary      string    `json:"summary"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	AllDay       bool      `json:"all_day"`
	Timezone     string    `json:"timezone"`
	Location     string    `json:"location"`
	Description  string    `json:"description"`
	Recurring    bool      `json:"recurring"`
	Status       string    `json:"status"`
	Organizer    string    `json:"organizer"`
	// Transparent means the event doesn't take up time, for free/busy.
	Transparent bool `json:"-"`
}

// ErrNoCalendarData means the data ended before any calendar began.
var ErrNoCalendarData = errors.New("parse iCalendar: there is no calendar in this data")

// Parse decodes a single iCalendar object.
func Parse(data []byte) (cal *goical.Calendar, err error) {
	// go-ical panics on some malformed lines, such as a parameter with no value
	// at the end of one. Calendar data comes from strangers, so that has to be
	// an error like any other.
	defer func() {
		if recover() != nil {
			cal, err = nil, errors.New("parse iCalendar: malformed line")
		}
	}()
	cal, err = goical.NewDecoder(bytes.NewReader(cleanLines(data))).Decode()
	if errors.Is(err, io.EOF) {
		return nil, ErrNoCalendarData
	}
	if err != nil {
		return nil, fmt.Errorf("parse iCalendar: %w", err)
	}
	return cal, nil
}

// lineEndings turns every kind of line ending into one the decoder reads. A real
// parcel service ends its lines with a bare carriage return.
var lineEndings = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// cleanLines repairs what real invites get wrong before any parsing can start. A
// byte order mark, which a real car rental service sends, would hide BEGIN.
func cleanLines(data []byte) []byte {
	return []byte(lineEndings.Replace(string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))))
}

// eventTemplate holds what all occurrences of an event have in common.
func eventTemplate(event *goical.Event) Occurrence {
	occurrence := Occurrence{
		Status:    "confirmed",
		Organizer: organizer(event),
	}
	for name, field := range map[string]*string{
		goical.PropUID:         &occurrence.UID,
		goical.PropSummary:     &occurrence.Summary,
		goical.PropLocation:    &occurrence.Location,
		goical.PropDescription: &occurrence.Description,
	} {
		*field = text(event, name)
	}
	if prop := event.Props.Get(goical.PropStatus); prop != nil {
		occurrence.Status = strings.ToLower(prop.Value)
	}
	if prop := event.Props.Get(goical.PropTransparency); prop != nil {
		occurrence.Transparent = strings.EqualFold(prop.Value, "TRANSPARENT")
	}
	return occurrence
}

// text reads a text property. go-ical's own reader treats the value as a list
// and returns what comes before the first unescaped comma, but mail clients do
// write unescaped commas, which would silently cut off a summary.
func text(event *goical.Event, name string) string {
	prop := event.Props.Get(name)
	if prop == nil {
		return ""
	}
	return textUnescaper.Replace(prop.Value)
}

var textUnescaper = strings.NewReplacer(`\\`, `\`, `\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";")

func organizer(event *goical.Event) string {
	prop := event.Props.Get(goical.PropOrganizer)
	if prop == nil {
		return ""
	}
	if len(prop.Value) >= len("mailto:") && strings.EqualFold(prop.Value[:len("mailto:")], "mailto:") {
		return prop.Value[len("mailto:"):]
	}
	return prop.Value
}
