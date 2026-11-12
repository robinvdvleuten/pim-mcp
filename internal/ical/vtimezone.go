package ical

import (
	"time"

	goical "github.com/emersion/go-ical"
	"github.com/robinvdvleuten/tzatziki"
)

// timezoneHorizonYears is how far past the event's first year a generated
// VTIMEZONE lists clock changes. After that the last offset is the one a reader
// that only has this definition will use. This project's own reader goes by the
// IANA name first, so it stays exact.
const timezoneHorizonYears = 10

// buildTimezone writes the VTIMEZONE for loc that RFC 5545 asks for beside any
// TZID, from 1 January of from's year up to the horizon.
func buildTimezone(loc *time.Location, from time.Time) (*goical.Component, error) {
	first := time.Date(from.Year(), time.January, 1, 0, 0, 0, 0, loc)
	end := time.Date(from.Year()+timezoneHorizonYears+1, time.January, 1, 0, 0, 0, 0, loc)
	return tzatziki.FromLocation(loc, first, end)
}
