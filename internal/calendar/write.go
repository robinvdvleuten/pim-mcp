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

// How much of a recurring event a change applies to.
const (
	ScopeSeries     = "series"
	ScopeOccurrence = "occurrence"
)

// UpdatedEvent reports a change. First is the event's first occurrence, for
// showing the result, and is empty if it has none left.
type UpdatedEvent struct {
	EventID string           `json:"event_id"`
	ETag    string           `json:"etag"`
	First   *ical.Occurrence `json:"first_occurrence,omitempty"`
}

// DeletedEvent reports a removal. ETag is set when an occurrence was removed and
// the rest of its series remains.
type DeletedEvent struct {
	EventID string `json:"event_id"`
	ETag    string `json:"etag,omitempty"`
	Deleted string `json:"deleted"`
}

// checkScope makes a mismatch an error. Quietly ignoring an occurrence_id would
// turn "delete this one" into "delete them all".
func checkScope(scope, occurrenceID string) error {
	switch {
	case scope == ScopeSeries && occurrenceID != "":
		return fmt.Errorf("scope %s applies to the whole event, so it takes no occurrence_id", ScopeSeries)
	case scope == ScopeOccurrence && occurrenceID == "":
		return fmt.Errorf("scope %s needs an occurrence_id", ScopeOccurrence)
	case scope != ScopeSeries && scope != ScopeOccurrence:
		return fmt.Errorf("scope must be %s or %s", ScopeSeries, ScopeOccurrence)
	}
	return nil
}

// current returns the stored event, if it is still the one the caller read. The
// server's copy is what a change gets applied to, so a stale caller finds out
// before anything is computed from it.
func (c *Client) current(ctx context.Context, eventID, etag string) (*Object, error) {
	if etag == "" {
		return nil, fmt.Errorf("event %s: an etag is required", eventID)
	}
	object, err := c.Get(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if object.ETag != etag {
		return nil, fmt.Errorf("event %s: %w", eventID, ErrConflict)
	}
	return object, nil
}

// UpdateEvent changes a stored event, if it is still the one the caller read.
func (c *Client) UpdateEvent(ctx context.Context, eventID, etag, scope, occurrenceID string, patch ical.EventPatch, loc *time.Location) (*UpdatedEvent, error) {
	if err := checkScope(scope, occurrenceID); err != nil {
		return nil, err
	}
	object, err := c.current(ctx, eventID, etag)
	if err != nil {
		return nil, err
	}
	var data []byte
	if scope == ScopeOccurrence {
		data, err = ical.EditOccurrence(object.Data, occurrenceID, patch, loc, time.Now())
	} else {
		data, err = ical.EditSeries(object.Data, patch, loc, time.Now())
	}
	if err != nil {
		return nil, err
	}
	newETag, err := c.Update(ctx, eventID, etag, data)
	if err != nil {
		return nil, err
	}
	first, err := ical.FirstOccurrence(data, loc)
	if err != nil {
		return nil, err
	}
	return &UpdatedEvent{EventID: eventID, ETag: newETag, First: first}, nil
}

// DeleteEvent removes a stored event or one of its occurrences, if it is still
// the one the caller read.
func (c *Client) DeleteEvent(ctx context.Context, eventID, etag, scope, occurrenceID string, loc *time.Location) (*DeletedEvent, error) {
	if err := checkScope(scope, occurrenceID); err != nil {
		return nil, err
	}
	if etag == "" {
		return nil, fmt.Errorf("event %s: an etag is required", eventID)
	}
	if scope == ScopeSeries {
		if err := c.Delete(ctx, eventID, etag); err != nil {
			return nil, err
		}
		return &DeletedEvent{EventID: eventID, Deleted: ScopeSeries}, nil
	}

	object, err := c.current(ctx, eventID, etag)
	if err != nil {
		return nil, err
	}
	data, err := ical.DeleteOccurrence(object.Data, occurrenceID, loc, time.Now())
	if err != nil {
		return nil, err
	}
	if data == nil {
		if err := c.Delete(ctx, eventID, etag); err != nil {
			return nil, err
		}
		return &DeletedEvent{EventID: eventID, Deleted: ScopeOccurrence}, nil
	}
	newETag, err := c.Update(ctx, eventID, etag, data)
	if err != nil {
		return nil, err
	}
	return &DeletedEvent{EventID: eventID, ETag: newETag, Deleted: ScopeOccurrence}, nil
}
