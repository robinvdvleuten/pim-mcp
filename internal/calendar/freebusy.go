package calendar

import (
	"context"
	"slices"
	"time"
)

// Interval is a span of time. It is comparable, with ==, only when both of its
// times are in the same zone.
type Interval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// FreeBusy holds when the user is busy. Skipped events may mean the answer is
// wrong, so they are named.
type FreeBusy struct {
	Busy    []Interval     `json:"busy"`
	Skipped []SkippedEvent `json:"skipped"`
}

// FreeBusy computes busy time from the events themselves, because not every
// server supports free-busy-query. Events marked as free, cancelled events, and
// events with no length don't count.
func (c *Client) FreeBusy(ctx context.Context, calendarIDs []string, start, end time.Time, loc *time.Location) (*FreeBusy, error) {
	list, err := c.Events(ctx, calendarIDs, start, end, loc)
	if err != nil {
		return nil, err
	}
	var busy []Interval
	for _, event := range list.Events {
		if event.Transparent || event.Status == "cancelled" || !event.End.After(event.Start) {
			continue
		}
		busy = append(busy, Interval{Start: laterOf(event.Start, start).In(loc), End: earlierOf(event.End, end).In(loc)})
	}
	return &FreeBusy{Busy: mergeBusy(busy), Skipped: list.Skipped}, nil
}

// mergeBusy sorts intervals and joins those that overlap or touch.
func mergeBusy(intervals []Interval) []Interval {
	sorted := slices.Clone(intervals)
	slices.SortFunc(sorted, func(a, b Interval) int { return a.Start.Compare(b.Start) })
	merged := []Interval{}
	for _, interval := range sorted {
		if last := len(merged) - 1; last >= 0 && !interval.Start.After(merged[last].End) {
			merged[last].End = laterOf(merged[last].End, interval.End)
			continue
		}
		merged = append(merged, interval)
	}
	return merged
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierOf(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
