package ical

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Zone data must not depend on the machine: pim-mcp is a single binary.

	goical "github.com/emersion/go-ical"
	"github.com/robinvdvleuten/tzatziki"
)

const (
	dateFormat     = "20060102"
	localFormat    = "20060102T150405"
	utcFormat      = "20060102T150405Z"
	maxDurationDay = 100 * 366
)

// zones turns the date values of one calendar into times.
type zones struct {
	floating *time.Location
	// resolver finds a TZID by name first and the calendar's own VTIMEZONE
	// second, and bounds the rules of a VTIMEZONE, which strangers write.
	resolver *tzatziki.Resolver
}

func newZones(cal *goical.Calendar, floating *time.Location) *zones {
	return &zones{floating: floating, resolver: tzatziki.NewResolver(cal)}
}

// dateTime reads a DATE or DATE-TIME value. The second result reports a DATE,
// which belongs to no zone and is placed at midnight in the user's.
func (z *zones) dateTime(prop *goical.Prop) (time.Time, bool, error) {
	switch {
	case len(prop.Value) == len(dateFormat):
		at, err := time.ParseInLocation(dateFormat, prop.Value, z.floating)
		return at, true, err
	case strings.HasSuffix(prop.Value, "Z"):
		at, err := time.ParseInLocation(utcFormat, prop.Value, time.UTC)
		return at, false, err
	}

	loc := z.floating
	if tzid := prop.Params.Get(goical.PropTimezoneID); tzid != "" {
		var err error
		if loc, err = z.resolver.Location(tzid); err != nil {
			return time.Time{}, false, err
		}
	}
	at, err := time.ParseInLocation(localFormat, prop.Value, loc)
	if err != nil {
		return time.Time{}, false, err
	}
	return firstInstant(at), false, nil
}

// firstInstant settles a wall clock time that happens twice, when clocks go
// back: RFC 5545 means the first, while Go may pick either.
func firstInstant(at time.Time) time.Time {
	_, offset := at.Zone()
	_, before := at.Add(-24 * time.Hour).Zone()
	if before <= offset {
		return at
	}
	earlier := at.Add(-time.Duration(before-offset) * time.Second)
	if _, earlierOffset := earlier.Zone(); earlierOffset == before {
		return earlier
	}
	return at
}

// LocalZone returns the user's zone with its IANA name where one can be found,
// because time.Local only calls itself "Local". That name can't be asked of Go,
// so it comes from where Go gets the zone itself: TZ, then /etc/localtime.
func LocalZone() *time.Location {
	tz, tzSet := os.LookupEnv("TZ")
	link, _ := os.Readlink("/etc/localtime")
	if name := localZoneName(tz, tzSet, link); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

func localZoneName(tz string, tzSet bool, localtime string) string {
	if tzSet {
		tz = strings.TrimPrefix(tz, ":")
		switch {
		case tz == "":
			return "UTC"
		case strings.HasPrefix(tz, "/"):
			return ""
		}
		return tz
	}
	if _, name, ok := strings.Cut(localtime, "zoneinfo/"); ok {
		return name
	}
	return ""
}

// ZoneName returns the name to report: empty when it isn't an IANA name.
func ZoneName(loc *time.Location) string {
	if loc == time.Local {
		return ""
	}
	return loc.String()
}

// span is how long an occurrence lasts. Days follow the calendar, so that a
// day stays one day across a clock change, while exact stays exact.
type span struct {
	days  int
	exact time.Duration
}

func (s span) end(start time.Time) time.Time {
	// AddDate goes through the wall clock, which would move a start that falls
	// in the repeated hour of a clock change, so it only runs when needed.
	if s.days != 0 {
		start = firstInstant(start.AddDate(0, 0, s.days))
	}
	return start.Add(s.exact)
}

var durationPattern = regexp.MustCompile(`^([+-])?P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func parseDuration(value string) (span, error) {
	match := durationPattern.FindStringSubmatch(value)
	if match == nil || value == "P" || strings.HasSuffix(value, "T") {
		return span{}, fmt.Errorf("bad duration %q", value)
	}
	if match[1] == "-" {
		return span{}, errors.New("ends before it starts")
	}
	var numbers [5]int
	for i := range numbers {
		if match[i+2] == "" {
			continue
		}
		number, err := strconv.Atoi(match[i+2])
		if err != nil || number > maxDurationDay {
			return span{}, fmt.Errorf("duration %q is too long", value)
		}
		numbers[i] = number
	}
	return span{
		days: numbers[0]*7 + numbers[1],
		exact: time.Duration(numbers[2])*time.Hour +
			time.Duration(numbers[3])*time.Minute +
			time.Duration(numbers[4])*time.Second,
	}, nil
}

// civilDays counts calendar days between two dates, ignoring clock changes.
func civilDays(from, to time.Time) int {
	fromYear, fromMonth, fromDay := from.Date()
	toYear, toMonth, toDay := to.Date()
	a := time.Date(fromYear, fromMonth, fromDay, 0, 0, 0, 0, time.UTC)
	b := time.Date(toYear, toMonth, toDay, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a) / (24 * time.Hour))
}
