package calendar

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

const timeRangeFormat = "20060102T150405Z"

// Event is one occurrence, with what a later write needs to find and guard it.
type Event struct {
	ical.Occurrence
	EventID    string `json:"event_id"`
	ETag       string `json:"etag"`
	CalendarID string `json:"calendar_id"`
}

// SkippedEvent reports a stored event that couldn't be read.
type SkippedEvent struct {
	EventID string `json:"event_id"`
	Reason  string `json:"reason"`
}

// EventList holds occurrences ordered by start. One broken event doesn't hide
// the rest of a calendar, but it is reported rather than silently left out.
type EventList struct {
	Events  []Event        `json:"events"`
	Skipped []SkippedEvent `json:"skipped"`
}

// Events returns the occurrences that overlap [start, end) in the given
// calendars, or in all of them if none are given. Floating times and all-day
// events are read in loc.
func (c *Client) Events(ctx context.Context, calendarIDs []string, start, end time.Time, loc *time.Location) (*EventList, error) {
	if err := ical.CheckRange(start, end); err != nil {
		return nil, err
	}
	if len(calendarIDs) == 0 {
		calendars, err := c.Calendars(ctx)
		if err != nil {
			return nil, err
		}
		for _, found := range calendars {
			calendarIDs = append(calendarIDs, found.ID)
		}
	}

	list := &EventList{Events: []Event{}, Skipped: []SkippedEvent{}}
	for _, calendarID := range calendarIDs {
		if err := c.collectEvents(ctx, list, calendarID, start, end, loc); err != nil {
			return nil, err
		}
		if len(list.Events) > ical.MaxOccurrences {
			return nil, ical.ErrTooManyOccurrences
		}
	}
	slices.SortStableFunc(list.Events, func(a, b Event) int {
		if order := a.Start.Compare(b.Start); order != 0 {
			return order
		}
		return strings.Compare(a.UID, b.UID)
	})
	return list, nil
}

func (c *Client) collectEvents(ctx context.Context, list *EventList, calendarID string, start, end time.Time, loc *time.Location) error {
	responses, err := c.queryEvents(ctx, calendarID, start, end)
	if err != nil {
		return err
	}
	for _, response := range responses {
		eventID, err := c.hrefPath(response.Href)
		if err != nil {
			list.Skipped = append(list.Skipped, SkippedEvent{EventID: response.Href, Reason: err.Error()})
			continue
		}
		prop := response.prop()
		occurrences, err := expandObject(prop.CalendarData, start, end, loc)
		if errors.Is(err, ical.ErrTooManyOccurrences) {
			return err
		}
		if err != nil {
			list.Skipped = append(list.Skipped, SkippedEvent{EventID: eventID, Reason: err.Error()})
			continue
		}
		for _, occurrence := range occurrences {
			list.Events = append(list.Events, Event{
				Occurrence: occurrence,
				EventID:    eventID,
				ETag:       unquoteETag(prop.ETag),
				CalendarID: calendarID,
			})
		}
	}
	return nil
}

func expandObject(data string, start, end time.Time, loc *time.Location) ([]ical.Occurrence, error) {
	if data == "" {
		return nil, errors.New("the server sent no calendar data")
	}
	cal, err := ical.Parse([]byte(data))
	if err != nil {
		return nil, err
	}
	return ical.Expand(cal, start, end, loc)
}

// queryEvents asks for a day more on each side than needed, because a server
// reads floating times in a zone of its own. Expanding then gets it exact.
func (c *Client) queryEvents(ctx context.Context, calendarID string, start, end time.Time) ([]davResponse, error) {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><c:calendar-query xmlns:d="%s" xmlns:c="%s">
<d:prop><d:getetag/><c:calendar-data/></d:prop>
<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
<c:time-range start="%s" end="%s"/>
</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`,
		nsDAV, nsCalDAV,
		start.UTC().AddDate(0, 0, -1).Format(timeRangeFormat),
		end.UTC().AddDate(0, 0, 1).Format(timeRangeFormat))
	return c.multistatus(ctx, "REPORT", calendarID, "1", body)
}

// FindByUID returns the stored event with a UID, or nil if there is none. The UID
// comes from an invite, so it's escaped for the query and compared again on the
// way back, as a server's text-match need not be exact.
func (c *Client) FindByUID(ctx context.Context, calendarID, uid string) (*Object, error) {
	var escaped strings.Builder
	if err := xml.EscapeText(&escaped, []byte(uid)); err != nil {
		return nil, fmt.Errorf("escape UID: %w", err)
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><c:calendar-query xmlns:d="%s" xmlns:c="%s">
<d:prop><d:getetag/><c:calendar-data/></d:prop>
<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
<c:prop-filter name="UID"><c:text-match collation="i;octet">%s</c:text-match></c:prop-filter>
</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`, nsDAV, nsCalDAV, escaped.String())
	responses, err := c.multistatus(ctx, "REPORT", calendarID, "1", body)
	if err != nil {
		return nil, err
	}
	for _, response := range responses {
		prop := response.prop()
		cal, err := ical.Parse([]byte(prop.CalendarData))
		if err != nil || !ical.HasUID(cal, uid) {
			continue
		}
		eventID, err := c.hrefPath(response.Href)
		if err != nil {
			return nil, err
		}
		return &Object{Path: eventID, ETag: unquoteETag(prop.ETag), Data: []byte(prop.CalendarData)}, nil
	}
	return nil, nil
}
