// Package importer turns calendar invites in email into calendar events.
package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/ical"
	"github.com/robinvdvleuten/pim-mcp/internal/mail"
)

// What importing would do with an event.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionCancel = "cancel"
	ActionSkip   = "skip"
)

// Event says what an invite holds and what importing does with it.
type Event struct {
	UID              string              `json:"uid"`
	Action           string              `json:"action"`
	Reason           string              `json:"reason"`
	Summary          string              `json:"summary,omitempty"`
	Start            time.Time           `json:"start,omitzero"`
	End              time.Time           `json:"end,omitzero"`
	AllDay           bool                `json:"all_day"`
	Timezone         string              `json:"timezone,omitempty"`
	Location         string              `json:"location,omitempty"`
	Recurring        bool                `json:"recurring"`
	Organizer        string              `json:"organizer,omitempty"`
	Sequence         int                 `json:"sequence"`
	ExistingEventID  string              `json:"existing_event_id,omitempty"`
	ExistingSequence *int                `json:"existing_sequence,omitempty"`
	BusyOverlap      []calendar.Interval `json:"busy_overlap,omitempty"`
	// EventID and ETag are set once the event has been written.
	EventID string `json:"event_id,omitempty"`
	ETag    string `json:"etag,omitempty"`
	// Error is why this event was not written, when others in the invite were.
	Error string `json:"error,omitempty"`
}

// Result is the outcome of a preview or an import.
type Result struct {
	Method  string `json:"method"`
	Written bool   `json:"written"`
	// Failed counts the events that carry an error. It is set only when other
	// events of the invite were written, so the result must not read as all-or-nothing.
	Failed int     `json:"failed,omitempty"`
	Events []Event `json:"events"`
}

// Preview works out what importing a message's invite would do. It writes nothing.
func Preview(ctx context.Context, messages *mail.Client, calendars *calendar.Client, messageID, calendarID string, loc *time.Location) (*Result, error) {
	invite, err := readInvite(ctx, messages, messageID, calendarID, loc)
	if err != nil {
		return nil, err
	}
	result := &Result{Method: invite.Method, Events: []Event{}}
	for _, incoming := range invite.Events {
		event, _, err := planEvent(ctx, calendars, invite.Method, incoming, calendarID, loc)
		if err != nil {
			return nil, err
		}
		result.Events = append(result.Events, *event)
	}
	return result, nil
}

// Import does what Preview shows, worked out again at this moment, so importing
// the same invite twice changes nothing the second time. If the stored event
// changes meanwhile, the write fails instead of overwriting it.
func Import(ctx context.Context, messages *mail.Client, calendars *calendar.Client, messageID, calendarID string, loc *time.Location) (*Result, error) {
	invite, err := readInvite(ctx, messages, messageID, calendarID, loc)
	if err != nil {
		return nil, err
	}
	return importInvite(ctx, calendars, invite, calendarID, loc)
}

func readInvite(ctx context.Context, messages *mail.Client, messageID, calendarID string, loc *time.Location) (*ical.Invite, error) {
	if calendarID == "" {
		return nil, errors.New("a calendar_id is required")
	}
	data, err := messages.CalendarPart(ctx, messageID)
	if err != nil {
		return nil, err
	}
	return ical.ParseInvite(data, loc)
}

// importInvite is Import after the invite has been read. Each event is looked
// up once, and the write that follows is conditional on the ETag of what that
// lookup found, so a change in between fails the write instead of losing it.
//
// Events are separate resources, so one failing does not stop the others. If
// nothing was written, the first error is returned. If something was, the
// result is returned without an error, as the agent must learn what was
// written. Once the context has ended, the remaining events are marked failed
// without trying them.
func importInvite(ctx context.Context, calendars *calendar.Client, invite *ical.Invite, calendarID string, loc *time.Location) (*Result, error) {
	result := &Result{Method: invite.Method, Events: []Event{}}
	var firstErr error
	fail := func(event *Event, err error) {
		// Nothing was written for this event, so it has no ID or ETag to report.
		event.EventID, event.ETag, event.Error = "", "", err.Error()
		result.Failed++
		result.Events = append(result.Events, *event)
	}
	for _, incoming := range invite.Events {
		if err := ctx.Err(); err != nil {
			fail(&Event{UID: incoming.UID, Sequence: incoming.Sequence}, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("event %q: %w", incoming.UID, err)
			}
			continue
		}
		event, existing, err := planEvent(ctx, calendars, invite.Method, incoming, calendarID, loc)
		if err != nil {
			fail(&Event{UID: incoming.UID, Sequence: incoming.Sequence}, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written, err := apply(ctx, calendars, invite, event, existing, calendarID, loc)
		if err != nil {
			err = fmt.Errorf("event %q: %w", incoming.UID, err)
			fail(event, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		result.Written = result.Written || written
		result.Events = append(result.Events, *event)
	}
	if firstErr != nil && !result.Written {
		return nil, firstErr
	}
	return result, nil
}

// apply writes what the plan decided. existing is the stored event the plan was
// made from, nil if there was none.
func apply(ctx context.Context, calendars *calendar.Client, invite *ical.Invite, event *Event, existing *calendar.Object, calendarID string, loc *time.Location) (bool, error) {
	switch event.Action {
	case ActionCreate:
		data, err := invite.Storable(event.UID)
		if err != nil {
			return false, err
		}
		event.EventID = objectPath(calendarID, event.UID)
		event.ETag, err = calendars.Create(ctx, event.EventID, data)
		return err == nil, err
	case ActionUpdate:
		// The plan only updates an event it found. If that is deleted before the
		// write, the server answers 404 or fails the precondition, and the
		// error says so.
		if existing == nil {
			return false, calendar.ErrEventNotFound
		}
		data, err := invite.MergeInto(existing.Data, event.UID)
		if err != nil {
			return false, err
		}
		event.EventID = existing.Path
		event.ETag, err = calendars.Update(ctx, existing.Path, existing.ETag, data)
		return err == nil, err
	case ActionCancel:
		return cancel(ctx, calendars, invite, event, existing, loc)
	default:
		return false, nil
	}
}

// cancel removes what the organizer called off: the event, or the occurrences
// named. An occurrence already gone is fine, as the outcome is the one wanted.
func cancel(ctx context.Context, calendars *calendar.Client, invite *ical.Invite, event *Event, existing *calendar.Object, loc *time.Location) (bool, error) {
	if existing == nil {
		return false, nil
	}
	event.EventID = existing.Path
	// An event deleted since the lookup is the outcome a cancellation wants.
	gone := func(written bool, err error) (bool, error) {
		if errors.Is(err, calendar.ErrEventNotFound) {
			event.Action, event.Reason = ActionSkip, "the event is already gone"
			return false, nil
		}
		return written, err
	}
	whole, ids, err := invite.CancelledOccurrences(event.UID)
	if err != nil {
		return false, err
	}
	if whole {
		return gone(true, calendars.Delete(ctx, existing.Path, existing.ETag))
	}

	data := existing.Data
	changed := false
	for _, id := range ids {
		remaining, err := ical.DeleteOccurrence(data, id, loc, time.Now())
		if errors.Is(err, ical.ErrNoSuchOccurrence) {
			continue
		}
		if err != nil {
			return false, err
		}
		changed = true
		if remaining == nil {
			return gone(true, calendars.Delete(ctx, existing.Path, existing.ETag))
		}
		data = remaining
	}
	if !changed {
		event.Action, event.Reason = ActionSkip, "these occurrences are already gone"
		return false, nil
	}
	event.ETag, err = calendars.Update(ctx, existing.Path, existing.ETag, data)
	return gone(err == nil, err)
}

// objectPath hashes the UID, because it comes from a stranger and could hold
// anything, such as a slash.
func objectPath(calendarID, uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return strings.TrimSuffix(calendarID, "/") + "/" + hex.EncodeToString(sum[:16]) + ".ics"
}

// planEvent works out what to do with one invite event. It also returns the
// stored event it found, so that the write need not look it up again.
func planEvent(ctx context.Context, calendars *calendar.Client, method string, incoming ical.InviteEvent, calendarID string, loc *time.Location) (*Event, *calendar.Object, error) {
	event := &Event{UID: incoming.UID, Sequence: incoming.Sequence}
	if first := incoming.First; first != nil {
		event.Summary, event.Start, event.End = first.Summary, first.Start, first.End
		event.AllDay, event.Timezone, event.Location = first.AllDay, first.Timezone, first.Location
		event.Recurring, event.Organizer = first.Recurring, first.Organizer
	}

	existing, err := calendars.FindByUID(ctx, calendarID, incoming.UID)
	if err != nil {
		return nil, nil, err
	}
	stored := 0
	if existing != nil {
		if stored, err = ical.StoredSequence(existing.Data); err != nil {
			return nil, nil, fmt.Errorf("read the stored copy of %q: %w", incoming.UID, err)
		}
		event.ExistingEventID = existing.Path
		event.ExistingSequence = &stored
	}
	event.Action, event.Reason = decide(method, incoming.Sequence, stored, existing != nil)

	// Time it would newly take up is what's worth warning about.
	if event.Action == ActionCreate && event.End.After(event.Start) {
		busy, err := calendars.FreeBusy(ctx, nil, event.Start, event.End, loc)
		if err != nil {
			return nil, nil, err
		}
		event.BusyOverlap = busy.Busy
	}
	return event, existing, nil
}

// decide applies RFC 5546's rule: a higher SEQUENCE is a newer version of the
// event. A cancellation needs no higher one, since it's sent for the version it
// cancels, but it shouldn't undo a change made after it.
func decide(method string, incoming, stored int, exists bool) (action, reason string) {
	switch {
	case method == ical.MethodCancel && !exists:
		return ActionSkip, "the event being cancelled isn't in this calendar"
	case method == ical.MethodCancel && incoming < stored:
		return ActionSkip, "the stored event is newer than this cancellation"
	case method == ical.MethodCancel:
		return ActionCancel, "the organizer cancelled this event"
	case !exists:
		return ActionCreate, "this event isn't in this calendar yet"
	case incoming > stored:
		return ActionUpdate, "this is a newer version of the stored event"
	case incoming == stored:
		return ActionSkip, "the stored event is already this version"
	default:
		return ActionSkip, "the stored event is newer than this invite"
	}
}
