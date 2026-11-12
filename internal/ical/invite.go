package ical

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/robinvdvleuten/tzatziki"
)

// The iTIP methods (RFC 5546) that can be imported.
const (
	MethodPublish = "PUBLISH"
	MethodRequest = "REQUEST"
	MethodCancel  = "CANCEL"
)

// ErrUnsupportedMethod means the calendar data is about scheduling, such as a
// reply from an attendee, rather than an event to store.
var ErrUnsupportedMethod = errors.New("this calendar message can't be imported")

// Invite is calendar data from an email, which comes from a stranger.
type Invite struct {
	Method string
	Events []InviteEvent

	cal *goical.Calendar
}

// InviteEvent is one event in an invite: every component with the same UID.
type InviteEvent struct {
	UID      string
	Sequence int
	// First is the earliest occurrence, for showing what's being imported. It's nil
	// if the event has none.
	First *Occurrence
}

// ParseInvite reads an invite, refusing what can't be understood or isn't meant
// to be stored.
func ParseInvite(data []byte, loc *time.Location) (*Invite, error) {
	cal, err := Parse(data)
	if err != nil {
		return nil, err
	}
	method := MethodPublish
	if prop := cal.Props.Get(goical.PropMethod); prop != nil {
		method = strings.ToUpper(prop.Value)
	}
	if !slices.Contains([]string{MethodPublish, MethodRequest, MethodCancel}, method) {
		return nil, fmt.Errorf("%w: its method is %s", ErrUnsupportedMethod, method)
	}

	fillMissingUIDs(cal)
	invite := &Invite{Method: method, cal: cal}
	for _, s := range groupSeries(cal.Events()) {
		event := InviteEvent{UID: s.uid, Sequence: seriesSequence(s)}
		single := calendarOfSeries(cal, s)
		encoded, err := encode(single)
		if err != nil {
			return nil, err
		}
		if event.First, err = FirstOccurrence(encoded, loc); err != nil {
			return nil, fmt.Errorf("event %q: %w", s.uid, err)
		}
		invite.Events = append(invite.Events, event)
	}
	if len(invite.Events) == 0 {
		return nil, errors.New("this invite has no events")
	}
	return invite, nil
}

// fillMissingUIDs gives an event without a UID one made from what it says, which a
// real hair salon's invites need. It leaves out DTSTAMP, which changes each time an
// invite is generated, so importing the same one twice still finds the first copy.
func fillMissingUIDs(cal *goical.Calendar) {
	for _, event := range cal.Events() {
		if text(&event, goical.PropUID) != "" {
			continue
		}
		hash := sha256.New()
		for _, name := range []string{goical.PropDateTimeStart, goical.PropDateTimeEnd, goical.PropSummary, goical.PropLocation, goical.PropOrganizer} {
			if prop := event.Props.Get(name); prop != nil {
				_, _ = fmt.Fprintf(hash, "%s=%s\n", name, prop.Value)
			}
		}
		event.Props.SetText(goical.PropUID, hex.EncodeToString(hash.Sum(nil)[:16])+"@pim-mcp")
	}
}

// calendarOfSeries makes a calendar of one series and the zones it may refer to.
func calendarOfSeries(cal *goical.Calendar, s *series) *goical.Calendar {
	single := goical.NewCalendar()
	for name, props := range cal.Props {
		if name != goical.PropMethod {
			single.Props[name] = props
		}
	}
	for _, child := range cal.Children {
		if child.Name == goical.CompTimezone {
			single.Children = append(single.Children, child)
		}
	}
	for _, event := range append(slices.Clone(s.masters), s.overrides...) {
		single.Children = append(single.Children, event.Component)
	}
	return single
}

func seriesSequence(s *series) int {
	highest := 0
	for _, event := range append(slices.Clone(s.masters), s.overrides...) {
		if prop := event.Props.Get(goical.PropSequence); prop != nil {
			if sequence, err := strconv.Atoi(prop.Value); err == nil && sequence > highest {
				highest = sequence
			}
		}
	}
	return highest
}

// StoredSequence returns the highest SEQUENCE in a stored event.
func StoredSequence(data []byte) (int, error) {
	cal, err := Parse(data)
	if err != nil {
		return 0, err
	}
	highest := 0
	for _, s := range groupSeries(cal.Events()) {
		highest = max(highest, seriesSequence(s))
	}
	return highest, nil
}

// HasUID reports whether a calendar holds an event with exactly this UID.
func HasUID(cal *goical.Calendar, uid string) bool {
	for _, s := range groupSeries(cal.Events()) {
		if s.uid == uid {
			return true
		}
	}
	return false
}

// textProperties hold a single text value. CATEGORIES holds a list, whose commas
// are meant.
var textProperties = []string{goical.PropSummary, goical.PropLocation, goical.PropDescription, goical.PropComment}

// Storable returns one event of the invite the way it should be stored: without
// METHOD, which CalDAV forbids, with its text properly escaped, and with Windows
// zone names replaced by IANA ones.
func (i *Invite) Storable(uid string) ([]byte, error) {
	s := i.series(uid)
	if s == nil {
		return nil, fmt.Errorf("this invite has no event %q", uid)
	}
	single := calendarOfSeries(i.cal, s)
	normalize(single.Component)
	return encode(single)
}

func (i *Invite) series(uid string) *series {
	for _, s := range groupSeries(i.cal.Events()) {
		if s.uid == uid {
			return s
		}
	}
	return nil
}

// normalize fixes what a stranger's mail client left behind. It works on a copy,
// as an invite's components are shared between its events' zones.
func normalize(component *goical.Component) {
	for i, child := range component.Children {
		component.Children[i] = cloneComponent(child)
		normalizeComponent(component.Children[i])
	}
}

func normalizeComponent(component *goical.Component) {
	for _, name := range textProperties {
		for i := range component.Props[name] {
			prop := &component.Props[name][i]
			prop.Value = escapeText(textUnescaper.Replace(prop.Value))
		}
	}
	for _, props := range component.Props {
		for i := range props {
			if mapped, ok := tzatziki.WindowsToIANA(props[i].Params.Get(goical.PropTimezoneID)); ok {
				props[i].Params.Set(goical.PropTimezoneID, mapped)
			}
		}
	}
	if component.Name == goical.CompTimezone {
		if prop := component.Props.Get(goical.PropTimezoneID); prop != nil {
			if mapped, ok := tzatziki.WindowsToIANA(prop.Value); ok {
				prop.Value = mapped
			}
		}
	}
	for _, child := range component.Children {
		normalizeComponent(child)
	}
}

func escapeText(text string) string {
	prop := goical.NewProp(goical.PropSummary)
	prop.SetText(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text))
	return prop.Value
}

// MergeInto returns what to store when an invite updates a stored event. An
// invite usually holds the whole event and replaces it. But an organizer may send
// just the occurrence that changed, and storing that alone would lose the series,
// so it goes in alongside what's there.
func (i *Invite) MergeInto(stored []byte, uid string) ([]byte, error) {
	s := i.series(uid)
	if s == nil {
		return nil, fmt.Errorf("this invite has no event %q", uid)
	}
	if len(s.masters) > 0 {
		return i.Storable(uid)
	}
	cal, err := Parse(stored)
	if err != nil {
		return nil, err
	}
	z := newZones(cal, time.UTC)
	incoming := newZones(i.cal, time.UTC)
	for _, override := range s.overrides {
		id, _, err := recurrenceID(incoming, override)
		if err != nil {
			return nil, err
		}
		cal.Children = slices.DeleteFunc(cal.Children, func(child *goical.Component) bool {
			prop := child.Props.Get(goical.PropRecurrenceID)
			if child.Name != goical.CompEvent || prop == nil {
				return false
			}
			existing, _, err := z.dateTime(prop)
			return err == nil && existing.Equal(id)
		})
		copied := cloneComponent(override.Component)
		normalizeComponent(copied)
		cal.Children = append(cal.Children, copied)
	}
	return encode(cal)
}

// CancelledOccurrences says what a CANCEL covers: the whole event, or the
// occurrences it names, as an occurrence_id of each.
func (i *Invite) CancelledOccurrences(uid string) (bool, []string, error) {
	s := i.series(uid)
	if s == nil {
		return false, nil, fmt.Errorf("this invite has no event %q", uid)
	}
	if len(s.masters) > 0 {
		return true, nil, nil
	}
	z := newZones(i.cal, time.UTC)
	ids := make([]string, 0, len(s.overrides))
	for _, override := range s.overrides {
		id, isDate, err := recurrenceID(z, override)
		if err != nil {
			return false, nil, err
		}
		ids = append(ids, occurrenceID(id, isDate))
	}
	return false, ids, nil
}
