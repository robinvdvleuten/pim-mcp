package calendar

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsApple  = "http://apple.com/ns/ical/"

	maxMultistatusBytes = 16 << 20
)

// Calendar is a collection of events. ID is its path on the server.
type Calendar struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color,omitempty"`
	ReadOnly bool   `json:"read_only"`
}

type davHref struct {
	Value string `xml:"href"`
}

type davProp struct {
	CurrentUserPrincipal davHref `xml:"DAV: current-user-principal"`
	CalendarHomeSet      davHref `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	DisplayName          string  `xml:"DAV: displayname"`
	ResourceType         struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	} `xml:"DAV: resourcetype"`
	Color        string `xml:"http://apple.com/ns/ical/ calendar-color"`
	ETag         string `xml:"DAV: getetag"`
	CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	Privileges   []struct {
		All          *struct{} `xml:"DAV: all"`
		Write        *struct{} `xml:"DAV: write"`
		WriteContent *struct{} `xml:"DAV: write-content"`
	} `xml:"DAV: current-user-privilege-set>privilege"`
	Components []struct {
		Name string `xml:"name,attr"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set>comp"`
}

type davResponse struct {
	Href      string `xml:"DAV: href"`
	Propstats []struct {
		Status string  `xml:"DAV: status"`
		Prop   davProp `xml:"DAV: prop"`
	} `xml:"DAV: propstat"`
}

// prop returns the properties the server found. Each propstat groups them by
// status, and the others are ones it doesn't have.
func (r davResponse) prop() davProp {
	for _, propstat := range r.Propstats {
		if strings.Contains(propstat.Status, " 200") {
			return propstat.Prop
		}
	}
	return davProp{}
}

// Calendars lists the user's calendars, sorted by name. It follows RFC 6764: the
// URL leads to the user's principal, which leads to the home of their calendars.
func (c *Client) Calendars(ctx context.Context) ([]Calendar, error) {
	principal, err := c.findHref(ctx, c.base.Path, "<d:current-user-principal/>", "principal",
		func(p davProp) string { return p.CurrentUserPrincipal.Value })
	if err != nil {
		return nil, err
	}
	home, err := c.findHref(ctx, principal, "<c:calendar-home-set/>", "calendar home",
		func(p davProp) string { return p.CalendarHomeSet.Value })
	if err != nil {
		return nil, err
	}

	responses, err := c.propfind(ctx, home, "1", `<d:displayname/><d:resourcetype/><a:calendar-color/>
		<d:current-user-privilege-set/><c:supported-calendar-component-set/>`)
	if err != nil {
		return nil, err
	}
	calendars := []Calendar{}
	for _, response := range responses {
		prop := response.prop()
		if prop.ResourceType.Calendar == nil || !holdsEvents(prop) {
			continue
		}
		id, err := c.hrefPath(response.Href)
		if err != nil {
			return nil, err
		}
		name := prop.DisplayName
		if name == "" {
			name = path.Base(id)
		}
		calendars = append(calendars, Calendar{ID: id, Name: name, Color: prop.Color, ReadOnly: isReadOnly(prop)})
	}
	slices.SortFunc(calendars, func(a, b Calendar) int { return strings.Compare(a.Name, b.Name) })
	return calendars, nil
}

// holdsEvents skips calendars only for tasks. A server that doesn't say what a
// calendar holds gets the benefit of the doubt.
func holdsEvents(prop davProp) bool {
	if len(prop.Components) == 0 {
		return true
	}
	return slices.ContainsFunc(prop.Components, func(comp struct {
		Name string `xml:"name,attr"`
	}) bool {
		return comp.Name == "VEVENT"
	})
}

// isReadOnly goes by what the server reports. A server that reports nothing is
// assumed writable, and refuses the write itself if that's wrong.
func isReadOnly(prop davProp) bool {
	if len(prop.Privileges) == 0 {
		return false
	}
	for _, privilege := range prop.Privileges {
		if privilege.All != nil || privilege.Write != nil || privilege.WriteContent != nil {
			return false
		}
	}
	return true
}

func (c *Client) findHref(ctx context.Context, target, prop, label string, get func(davProp) string) (string, error) {
	responses, err := c.propfind(ctx, target, "0", prop)
	if err != nil {
		return "", err
	}
	for _, response := range responses {
		if href := get(response.prop()); href != "" {
			return c.hrefPath(href)
		}
	}
	return "", fmt.Errorf("the server at %s reports no %s", target, label)
}

// hrefPath returns the path an href points at. Everything is reached through
// the configured server, so a home elsewhere can't be used, and the password
// shouldn't go there anyway.
func (c *Client) hrefPath(href string) (string, error) {
	u, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("server sent a bad href: %w", err)
	}
	if u.Host != "" && u.Host != c.base.Host {
		return "", fmt.Errorf("the server points to %s, and only %s is configured", u.Host, c.base.Host)
	}
	return u.Path, nil
}

func (c *Client) propfind(ctx context.Context, target, depth, props string) ([]davResponse, error) {
	body := `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="` + nsDAV + `" xmlns:c="` + nsCalDAV +
		`" xmlns:a="` + nsApple + `"><d:prop>` + props + `</d:prop></d:propfind>`
	return c.multistatus(ctx, "PROPFIND", target, depth, body)
}

// multistatus sends a WebDAV request and reads the responses it gets back.
func (c *Client) multistatus(ctx context.Context, method, target, depth, body string) ([]davResponse, error) {
	req, err := c.newRequest(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusMultiStatus:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%s %s: %w", method, target, ErrLoginRefused)
	default:
		return nil, fmt.Errorf("%s %s: %s", method, target, resp.Status)
	}

	var status struct {
		Responses []davResponse `xml:"DAV: response"`
	}
	decoder := xml.NewDecoder(io.LimitReader(resp.Body, maxMultistatusBytes))
	if err := decoder.Decode(&status); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("%s %s: parse response: %w", method, target, err)
	}
	return status.Responses, nil
}
