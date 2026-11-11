package calendar

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrOutsideCalendars means an ID points somewhere other than the user's
// calendars. IDs come from an agent that may have been misled, and the same
// login can reach files and contacts on some servers.
var ErrOutsideCalendars = errors.New("that id is not inside the user's calendars")

// homeSet returns the path that holds the user's calendars, found once per
// client by way of the principal (RFC 6764). A failure isn't kept, so a call
// that was cancelled doesn't make the next one fail too. One call looks while
// the others wait, and a waiter gives up when its own context ends.
func (c *Client) homeSet(ctx context.Context) (string, error) {
	for {
		c.homeMu.Lock()
		if c.home != "" {
			home := c.home
			c.homeMu.Unlock()
			return home, nil
		}
		if looking := c.homeLooking; looking != nil {
			c.homeMu.Unlock()
			select {
			case <-looking:
				// The lookup ended, perhaps in failure. Check again.
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		looking := make(chan struct{})
		c.homeLooking = looking
		c.homeMu.Unlock()

		home, err := c.lookupHome(ctx)

		c.homeMu.Lock()
		if err == nil {
			c.home = home
		}
		c.homeLooking = nil
		c.homeMu.Unlock()
		close(looking)
		return home, err
	}
}

func (c *Client) lookupHome(ctx context.Context) (string, error) {
	principal, err := c.findHref(ctx, c.base.Path, "<d:current-user-principal/>", "principal",
		func(p davProp) string { return p.CurrentUserPrincipal.Value })
	if err != nil {
		return "", err
	}
	return c.findHref(ctx, principal, "<c:calendar-home-set/>", "calendar home",
		func(p davProp) string { return p.CalendarHomeSet.Value })
}

// calendarPath checks that id is a calendar: strictly below the calendar home.
// It returns the cleaned path, which is the one to send, so that what was
// checked is what the server gets.
func (c *Client) calendarPath(ctx context.Context, id string) (string, error) {
	return c.scoped(ctx, id, 1)
}

// eventPath checks that id is an event: below a calendar, so at least two
// levels below the calendar home.
func (c *Client) eventPath(ctx context.Context, id string) (string, error) {
	return c.scoped(ctx, id, 2)
}

func (c *Client) scoped(ctx context.Context, id string, levels int) (string, error) {
	// A relative id would be resolved against the server URL, not the home.
	if !strings.HasPrefix(id, "/") {
		return "", fmt.Errorf("%w: %q", ErrOutsideCalendars, id)
	}
	home, err := c.homeSet(ctx)
	if err != nil {
		return "", err
	}
	cleaned := path.Clean(id)
	if !within(path.Clean(home), cleaned, levels) {
		return "", fmt.Errorf("%w: %q", ErrOutsideCalendars, id)
	}
	// A server may treat a collection URL without its slash differently.
	if strings.HasSuffix(id, "/") {
		cleaned += "/"
	}
	return cleaned, nil
}

// within reports whether target is at least levels path segments below home.
// Both must be cleaned. Matching on whole segments keeps /home/user2 out of
// /home/user.
func within(home, target string, levels int) bool {
	prefix := strings.TrimSuffix(home, "/") + "/"
	rest, ok := strings.CutPrefix(target, prefix)
	if !ok || rest == "" {
		return false
	}
	return strings.Count(rest, "/")+1 >= levels
}
