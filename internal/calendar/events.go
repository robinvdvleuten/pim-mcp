package calendar

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	if len(calendarIDs) > 0 {
		// All of them are checked before the first one is queried.
		checked := make([]string, len(calendarIDs))
		for i, id := range calendarIDs {
			var err error
			if checked[i], err = c.calendarPath(ctx, id); err != nil {
				return nil, err
			}
		}
		calendarIDs = checked
	} else {
		calendars, err := c.Calendars(ctx)
		if err != nil {
			return nil, err
		}
		for _, found := range calendars {
			calendarIDs = append(calendarIDs, found.ID)
		}
	}

	// Each calendar fills its own slot, and the slots are joined in the order
	// given, so the answer doesn't depend on which request finishes first.
	parts := make([]*EventList, len(calendarIDs))
	var total atomic.Int64
	err := forEach(ctx, maxCalendarsInFlight, len(calendarIDs), func(ctx context.Context, i int) error {
		part, err := c.collectEvents(ctx, calendarIDs[i], start, end, loc)
		if err != nil {
			return err
		}
		// Stop the others as soon as the whole answer is known to be too big.
		if total.Add(int64(len(part.Events))) > ical.MaxOccurrences {
			return ical.ErrTooManyOccurrences
		}
		parts[i] = part
		return nil
	})
	if err != nil {
		return nil, err
	}

	list := &EventList{Events: []Event{}, Skipped: []SkippedEvent{}}
	for _, part := range parts {
		list.Events = append(list.Events, part.Events...)
		list.Skipped = append(list.Skipped, part.Skipped...)
	}
	slices.SortStableFunc(list.Events, func(a, b Event) int {
		if order := a.Start.Compare(b.Start); order != 0 {
			return order
		}
		return strings.Compare(a.UID, b.UID)
	})
	return list, nil
}

// maxCalendarsInFlight bounds how many calendars are read at once, so a user
// with many calendars neither floods the server nor holds all their answers in
// memory at the same time.
const maxCalendarsInFlight = 4

// forEach runs fn for 0..n-1 with at most limit running at once. It returns the
// failure with the lowest index, whichever happened first. So that this holds,
// a failure cancels only the calls after it: the ones before it run to the end,
// as one of them may fail too.
func forEach(ctx context.Context, limit, n int, fn func(ctx context.Context, i int) error) error {
	var (
		mu      sync.Mutex
		cancels = make([]context.CancelFunc, n)
		failed  = n // The lowest index that has failed so far.
		errs    = make([]error, n)
		slots   = make(chan struct{}, limit)
		wg      sync.WaitGroup
	)
	for i := range n {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		mu.Lock()
		if ctx.Err() != nil || i > failed {
			mu.Unlock()
			if ctx.Err() == nil {
				<-slots
			}
			break
		}
		callCtx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		mu.Unlock()

		wg.Go(func() {
			defer func() { <-slots }()
			defer cancel()
			err := fn(callCtx, i)
			if err == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			errs[i] = err
			if i < failed {
				failed = i
				for _, later := range cancels[i+1:] {
					if later != nil {
						later()
					}
				}
			}
		})
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c *Client) collectEvents(ctx context.Context, calendarID string, start, end time.Time, loc *time.Location) (*EventList, error) {
	list := &EventList{Events: []Event{}, Skipped: []SkippedEvent{}}
	responses, err := c.queryEvents(ctx, calendarID, start, end)
	if err != nil {
		return nil, err
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
			return nil, err
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
	return list, nil
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
// reads floating times in a zone of its own. Expanding then gets it exact. It also
// asks for every event with a changed occurrence, whatever its dates: Nextcloud's
// range filter goes by the series, and misses an occurrence moved outside of it.
// The two requests are independent, so they run together.
func (c *Client) queryEvents(ctx context.Context, calendarID string, start, end time.Time) ([]davResponse, error) {
	filters := [2]string{
		fmt.Sprintf(`<c:time-range start="%s" end="%s"/>`,
			start.UTC().AddDate(0, 0, -1).Format(timeRangeFormat),
			end.UTC().AddDate(0, 0, 1).Format(timeRangeFormat)),
		`<c:prop-filter name="RECURRENCE-ID"/>`,
	}
	var found [2][]davResponse
	err := forEach(ctx, len(filters), len(filters), func(ctx context.Context, i int) error {
		var err error
		found[i], err = c.reportEvents(ctx, calendarID, filters[i])
		return err
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var responses []davResponse
	for _, response := range slices.Concat(found[0], found[1]) {
		if !seen[response.Href] {
			seen[response.Href] = true
			responses = append(responses, response)
		}
	}
	return responses, nil
}

func (c *Client) reportEvents(ctx context.Context, calendarID, filter string) ([]davResponse, error) {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><c:calendar-query xmlns:d="%s" xmlns:c="%s">
<d:prop><d:getetag/><c:calendar-data/></d:prop>
<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">%s</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`,
		nsDAV, nsCalDAV, filter)
	return c.multistatus(ctx, "REPORT", calendarID, "1", body)
}

// FindByUID returns the stored event with a UID, or nil if there is none. The UID
// comes from an invite, so it's escaped for the query and compared again on the
// way back, as a server's text-match need not be exact.
func (c *Client) FindByUID(ctx context.Context, calendarID, uid string) (*Object, error) {
	calendarID, err := c.calendarPath(ctx, calendarID)
	if err != nil {
		return nil, err
	}
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
