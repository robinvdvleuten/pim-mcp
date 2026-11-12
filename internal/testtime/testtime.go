// Package testtime lets tests bound the cost of hostile input against a
// reference measured on the same machine, instead of against a fixed
// duration that a slow CI runner or the race detector would break.
package testtime

import "time"

// Fastest runs f once to warm up, then twice more, and returns the shorter of
// those two runs, so one pause of the machine doesn't decide a timing test.
func Fastest(f func()) time.Duration {
	f()
	best := time.Duration(1<<63 - 1)
	for range 2 {
		started := time.Now()
		f()
		best = min(best, time.Since(started))
	}
	return best
}
