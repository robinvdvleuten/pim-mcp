package calendar

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const multistatusHead = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:a="http://apple.com/ns/ical/">`

func principalResponse(href string) string {
	return multistatusHead + `<d:response><d:href>/</d:href><d:propstat><d:prop>
<d:current-user-principal><d:href>` + href + `</d:href></d:current-user-principal>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
}

func homeResponse(href string) string {
	return multistatusHead + `<d:response><d:href>/principals/me/</d:href><d:propstat><d:prop>
<c:calendar-home-set><d:href>` + href + `</d:href></c:calendar-home-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
}

// davServer answers the three lookups in order: principal, home set, calendars.
func davServer(t *testing.T, responses map[string]string) (*Client, *[]string) {
	t.Helper()
	var depths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Errorf("method = %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "propfind") {
			t.Errorf("body = %q", body)
		}
		depths = append(depths, r.URL.Path+" depth "+r.Header.Get("Depth"))
		response, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL + "/dav/", Username: "me", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return client, &depths
}

func TestCalendarsReadsEveryField(t *testing.T) {
	client, depths := davServer(t, map[string]string{
		"/dav/":           principalResponse("/principals/me/"),
		"/principals/me/": homeResponse("/calendars/me/"),
		"/calendars/me/": multistatusHead + `
<d:response><d:href>/calendars/me/</d:href><d:propstat><d:prop>
  <d:displayname>Home</d:displayname><d:resourcetype><d:collection/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>

<d:response><d:href>/calendars/me/work/</d:href><d:propstat><d:prop>
  <d:displayname>Work</d:displayname>
  <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
  <a:calendar-color>#FF0000FF</a:calendar-color>
  <d:current-user-privilege-set><d:privilege><d:read/></d:privilege><d:privilege><d:write/></d:privilege></d:current-user-privilege-set>
  <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>

<d:response><d:href>/calendars/me/holidays/</d:href><d:propstat><d:prop>
  <d:displayname>Holidays</d:displayname>
  <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
  <d:current-user-privilege-set><d:privilege><d:read/></d:privilege></d:current-user-privilege-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>

<d:response><d:href>/calendars/me/tasks/</d:href><d:propstat><d:prop>
  <d:displayname>Tasks</d:displayname>
  <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
  <c:supported-calendar-component-set><c:comp name="VTODO"/></c:supported-calendar-component-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>

<d:response><d:href>/calendars/me/inbox/</d:href><d:propstat><d:prop>
  <d:displayname>Inbox</d:displayname>
  <d:resourcetype><d:collection/><c:schedule-inbox/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>

<d:response><d:href>/calendars/me/caf%C3%A9%20team/</d:href><d:propstat><d:prop>
  <d:displayname/>
</d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat><d:propstat><d:prop>
  <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
  <d:current-user-privilege-set><d:privilege><d:all/></d:privilege></d:current-user-privilege-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>

<d:response><d:href>/calendars/me/content/</d:href><d:propstat><d:prop>
  <d:displayname>Content only</d:displayname>
  <d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
  <d:current-user-privilege-set><d:privilege><d:write-content/></d:privilege></d:current-user-privilege-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`,
	})

	got, err := client.Calendars(t.Context())
	if err != nil {
		t.Fatalf("calendars: %v", err)
	}

	want := []Calendar{
		{ID: "/calendars/me/content/", Name: "Content only"},
		{ID: "/calendars/me/holidays/", Name: "Holidays", ReadOnly: true},
		{ID: "/calendars/me/work/", Name: "Work", Color: "#FF0000FF"},
		{ID: "/calendars/me/café team/", Name: "café team"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("calendar %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got, want := strings.Join(*depths, ", "), "/dav/ depth 0, /principals/me/ depth 0, /calendars/me/ depth 1"; got != want {
		t.Errorf("requests = %s, want %s", got, want)
	}
}

func TestCalendarsAcceptsAbsoluteHrefsOnTheSameHost(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(principalResponse(serverURL + "/principals/me/")))
		case "/principals/me/":
			_, _ = w.Write([]byte(homeResponse(serverURL + "/calendars/me/")))
		default:
			_, _ = w.Write([]byte(multistatusHead + `<d:response><d:href>` + serverURL + `/calendars/me/work/</d:href><d:propstat><d:prop>
<d:displayname>Work</d:displayname><d:resourcetype><c:calendar/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL
	client, err := New(Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := client.Calendars(t.Context())
	if err != nil {
		t.Fatalf("calendars: %v", err)
	}

	if len(got) != 1 || got[0].ID != "/calendars/me/work/" {
		t.Errorf("got %+v", got)
	}
}

func TestCalendarsFailures(t *testing.T) {
	tests := []struct {
		name      string
		responses map[string]string
		want      string
	}{
		{"no principal", map[string]string{"/dav/": multistatusHead + `</d:multistatus>`}, "principal"},
		{"no home set", map[string]string{"/dav/": principalResponse("/p/"), "/p/": multistatusHead + `</d:multistatus>`}, "home"},
		{"home on another server", map[string]string{"/dav/": principalResponse("/p/"), "/p/": homeResponse("https://other.example.org/calendars/")}, "other.example.org"},
		{"not XML", map[string]string{"/dav/": "<html"}, "/dav/"},
		{"server error part way", map[string]string{"/dav/": principalResponse("/p/")}, "404"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := davServer(t, tt.responses)

			_, err := client.Calendars(t.Context())

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestCalendarsLoginRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL, Username: "me", Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Calendars(t.Context())

	if !errors.Is(err, ErrLoginRefused) {
		t.Errorf("err = %v, want ErrLoginRefused", err)
	}
}
