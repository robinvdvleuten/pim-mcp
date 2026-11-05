package calendar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

// CreateEvent adds a new single event to a calendar.
func (c *Client) CreateEvent(ctx context.Context, calendarID string, fields ical.EventFields, loc *time.Location) (*Event, error) {
	if err := checkFields(fields); err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("make event id: %w", err)
	}
	uid := hex.EncodeToString(random) + "@pim-mcp"
	data, err := ical.BuildEvent(uid, fields, time.Now())
	if err != nil {
		return nil, err
	}
	eventID := strings.TrimSuffix(calendarID, "/") + "/" + uid + ".ics"
	etag, err := c.Create(ctx, eventID, data)
	if err != nil {
		return nil, err
	}
	return storedEvent(data, calendarID, eventID, etag, fields, loc)
}

func checkFields(fields ical.EventFields) error {
	if strings.TrimSpace(fields.Summary) == "" {
		return fmt.Errorf("an event needs a summary")
	}
	return ical.CheckRange(fields.Start, fields.End)
}

// storedEvent reads back what was written, so the answer shows the event the way
// any later read will.
func storedEvent(data []byte, calendarID, eventID, etag string, fields ical.EventFields, loc *time.Location) (*Event, error) {
	start, end := fields.Start, fields.End
	if fields.AllDay {
		// Dates count in the user's zone, whichever zone they were given in.
		start, end = midnight(start, loc), midnight(end, loc)
	}
	occurrences, err := expandObject(string(data), start, end, loc)
	if err != nil {
		return nil, err
	}
	if len(occurrences) == 0 {
		return nil, fmt.Errorf("event %s was stored, but can't be read back", eventID)
	}
	return &Event{Occurrence: occurrences[0], EventID: eventID, ETag: etag, CalendarID: calendarID}, nil
}

func midnight(day time.Time, loc *time.Location) time.Time {
	year, month, date := day.Date()
	return time.Date(year, month, date, 0, 0, 0, 0, loc)
}
