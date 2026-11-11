package importer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
	"github.com/robinvdvleuten/pim-mcp/internal/ical"
)

const (
	testCalendar = "/cal/work/"
	testUID      = "meeting-1@example.org"
)

func inviteText(method string, sequence string) string {
	return strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN", "METHOD:" + method,
		"BEGIN:VEVENT", "UID:" + testUID, "DTSTAMP:20261101T120000Z", "DTSTART:20261110T120000Z",
		"DTEND:20261110T130000Z", "SUMMARY:Meeting", "SEQUENCE:" + sequence,
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n")
}

// fakeCalDAV holds at most one event at the path an import would use. It counts
// the UID lookups, and can change the event after one, as a user on a phone would.
type fakeCalDAV struct {
	client *calendar.Client

	mu       sync.Mutex
	data     string // The stored event, empty if there is none.
	etag     string
	lookups  int
	writes   []string
	afterGet func(f *fakeCalDAV) // Runs after a UID lookup, under mu.
	missing  int                 // Status for a write to an event that is gone.

	// For invites of several events. Events other than testUID are not stored.
	putStatus    map[string]int // Status to fail a write with, by UID in the written body.
	reportStatus map[string]int // Status to fail a UID lookup with, by UID in the query.

	// busy is an event of another calendar item, if set. It makes the calendar
	// list non-empty and is what a time-range query returns, for free/busy.
	busy string
}

func newFakeCalDAV(t *testing.T, stored string) *fakeCalDAV {
	t.Helper()
	f := &fakeCalDAV{data: stored, etag: "v1", missing: http.StatusPreconditionFailed}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	client, err := calendar.New(calendar.Config{URL: server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	f.client = client
	return f
}

const head = `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">`

func (f *fakeCalDAV) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == "PROPFIND" && r.URL.Path == "/":
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(head + `<d:response><d:href>/</d:href><d:propstat><d:prop><d:current-user-principal><d:href>/p/</d:href></d:current-user-principal></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	case r.Method == "PROPFIND" && r.URL.Path == "/p/":
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(head + `<d:response><d:href>/p/</d:href><d:propstat><d:prop><c:calendar-home-set><d:href>/cal/</d:href></c:calendar-home-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	case r.Method == "PROPFIND":
		// The calendar list, which is empty so that no free/busy is asked for.
		w.WriteHeader(http.StatusMultiStatus)
		if f.busy == "" {
			_, _ = w.Write([]byte(head + `</d:multistatus>`))
			return
		}
		_, _ = w.Write([]byte(head + `<d:response><d:href>` + testCalendar + `</d:href><d:propstat><d:prop><d:displayname>Work</d:displayname><d:resourcetype><c:calendar/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	case r.Method == "REPORT" && strings.Contains(string(body), "<c:filter><c:comp-filter name=\"VCALENDAR\"><c:comp-filter name=\"VEVENT\"><c:time-range"):
		// A free/busy query, which is not a UID lookup and so is not counted.
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(head + `<d:response><d:href>` + testCalendar + `busy.ics</d:href><d:propstat><d:prop><d:getetag>"b1"</d:getetag><c:calendar-data>` + f.busy + `</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	case r.Method == "REPORT" && strings.Contains(string(body), `<c:prop-filter name="RECURRENCE-ID"/>`):
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(head + `</d:multistatus>`))
	case r.Method == "REPORT":
		f.lookups++
		for uid, status := range f.reportStatus {
			if strings.Contains(string(body), uid) {
				w.WriteHeader(status)
				return
			}
		}
		w.WriteHeader(http.StatusMultiStatus)
		response := ""
		if f.data != "" {
			response = `<d:response><d:href>` + f.path() + `</d:href><d:propstat><d:prop><d:getetag>"` + f.etag +
				`"</d:getetag><c:calendar-data>` + strings.ReplaceAll(f.data, "&", "&amp;") + `</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
		}
		_, _ = w.Write([]byte(head + response + `</d:multistatus>`))
		if f.afterGet != nil {
			f.afterGet(f)
		}
	case r.Method == http.MethodPut || r.Method == http.MethodDelete:
		f.writes = append(f.writes, r.Method+" If-Match="+r.Header.Get("If-Match")+" If-None-Match="+r.Header.Get("If-None-Match"))
		for uid, status := range f.putStatus {
			if strings.Contains(string(body), "UID:"+uid) {
				w.WriteHeader(status)
				return
			}
		}
		switch {
		case r.URL.Path != f.path():
			w.Header().Set("ETag", `"other"`)
			w.WriteHeader(http.StatusNoContent)
		case r.Header.Get("If-None-Match") == "*" && f.data != "", r.Header.Get("If-Match") != "" && f.data != "" && r.Header.Get("If-Match") != `"`+f.etag+`"`:
			w.WriteHeader(http.StatusPreconditionFailed)
		case r.Header.Get("If-Match") != "" && f.data == "":
			w.WriteHeader(f.missing)
		case r.Method == http.MethodDelete:
			f.data = ""
			w.WriteHeader(http.StatusNoContent)
		default:
			f.data, f.etag = string(body), "v2"
			w.Header().Set("ETag", `"v2"`)
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// path is where the stored event lives.
func (f *fakeCalDAV) path() string { return objectPath(testCalendar, testUID) }

func (f *fakeCalDAV) stored() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func run(t *testing.T, f *fakeCalDAV, method, sequence string) (*Result, error) {
	t.Helper()
	return runText(t, f, inviteText(method, sequence))
}

func runText(t *testing.T, f *fakeCalDAV, text string) (*Result, error) {
	t.Helper()
	invite, err := ical.ParseInvite([]byte(text), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return importInvite(t.Context(), f.client, invite, testCalendar, time.UTC)
}

func TestImportLooksEachEventUpOnce(t *testing.T) {
	stored := inviteText("PUBLISH", "0")
	stored = strings.Replace(stored, "METHOD:PUBLISH\r\n", "", 1)
	tests := []struct {
		name     string
		stored   string
		method   string
		sequence string
		action   string
	}{
		{"create", "", "REQUEST", "0", ActionCreate},
		{"update", stored, "REQUEST", "1", ActionUpdate},
		{"cancel", stored, "CANCEL", "1", ActionCancel},
		{"skip", stored, "REQUEST", "0", ActionSkip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeCalDAV(t, tt.stored)

			result, err := run(t, f, tt.method, tt.sequence)
			if err != nil {
				t.Fatalf("import: %v", err)
			}

			if got := result.Events[0].Action; got != tt.action {
				t.Errorf("action = %q", got)
			}
			if f.lookups != 1 {
				t.Errorf("UID lookups = %d, want 1", f.lookups)
			}
		})
	}
}

func TestImportUpdateIsConditionalOnWhatWasFound(t *testing.T) {
	f := newFakeCalDAV(t, strings.Replace(inviteText("PUBLISH", "0"), "METHOD:PUBLISH\r\n", "", 1))

	if _, err := run(t, f, "REQUEST", "1"); err != nil {
		t.Fatalf("import: %v", err)
	}

	if len(f.writes) != 1 || f.writes[0] != `PUT If-Match="v1" If-None-Match=` {
		t.Errorf("writes = %q", f.writes)
	}
}

func TestImportDoesNotOverwriteAChangeMadeAfterTheLookup(t *testing.T) {
	f := newFakeCalDAV(t, strings.Replace(inviteText("PUBLISH", "0"), "METHOD:PUBLISH\r\n", "", 1))
	f.afterGet = func(f *fakeCalDAV) {
		f.etag, f.data = "v-phone", strings.Replace(f.data, "Meeting", "Edited on a phone", 1)
	}

	_, err := run(t, f, "REQUEST", "1")

	if !errors.Is(err, calendar.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(f.stored(), "Edited on a phone") {
		t.Error("the edit made in between was overwritten")
	}
}

func TestImportUpdateOfAnEventDeletedAfterTheLookup(t *testing.T) {
	tests := map[string]struct {
		status int
		want   error
	}{
		"precondition failed": {http.StatusPreconditionFailed, calendar.ErrConflict},
		"not found":           {http.StatusNotFound, calendar.ErrEventNotFound},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeCalDAV(t, strings.Replace(inviteText("PUBLISH", "0"), "METHOD:PUBLISH\r\n", "", 1))
			f.missing = tt.status
			f.afterGet = func(f *fakeCalDAV) { f.data = "" }

			_, err := run(t, f, "REQUEST", "1")

			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestImportCancelOfAnEventDeletedAfterTheLookupIsFine(t *testing.T) {
	f := newFakeCalDAV(t, strings.Replace(inviteText("PUBLISH", "0"), "METHOD:PUBLISH\r\n", "", 1))
	f.missing = http.StatusNotFound
	f.afterGet = func(f *fakeCalDAV) { f.data = "" }

	result, err := run(t, f, "CANCEL", "1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if result.Written || result.Events[0].Action != ActionSkip {
		t.Errorf("result = %+v", result)
	}
}

func TestImportCancelDoesNotDeleteAChangeMadeAfterTheLookup(t *testing.T) {
	f := newFakeCalDAV(t, strings.Replace(inviteText("PUBLISH", "0"), "METHOD:PUBLISH\r\n", "", 1))
	f.afterGet = func(f *fakeCalDAV) { f.etag = "v-phone" }

	_, err := run(t, f, "CANCEL", "1")

	if !errors.Is(err, calendar.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if f.stored() == "" {
		t.Error("the event was deleted")
	}
}

const secondUID = "meeting-2@example.org"

// twoEvents is an invite of two new events: testUID, then secondUID.
func twoEvents(t *testing.T) *ical.Invite { t.Helper(); return events(t, secondUID) }

// events is an invite of new events: testUID, then one for each UID given.
func events(t *testing.T, uids ...string) *ical.Invite {
	t.Helper()
	text := inviteText("REQUEST", "0")
	one := text[strings.Index(text, "BEGIN:VEVENT"):strings.Index(text, "END:VCALENDAR")]
	for _, uid := range uids {
		text = strings.Replace(text, "END:VCALENDAR", strings.Replace(one, "UID:"+testUID, "UID:"+uid, 1)+"END:VCALENDAR", 1)
	}
	invite, err := ical.ParseInvite([]byte(text), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(invite.Events) != 1+len(uids) {
		t.Fatalf("events = %d, want %d", len(invite.Events), 1+len(uids))
	}
	return invite
}

func TestImportKeepsGoingWhenALaterEventFails(t *testing.T) {
	f := newFakeCalDAV(t, "")
	f.putStatus = map[string]int{secondUID: http.StatusPreconditionFailed}

	result, err := importInvite(t.Context(), f.client, twoEvents(t), testCalendar, time.UTC)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	first, second := result.Events[0], result.Events[1]
	if !result.Written || result.Failed != 1 {
		t.Errorf("written = %v, failed = %d", result.Written, result.Failed)
	}
	if first.ETag == "" || first.Error != "" {
		t.Errorf("first = %+v", first)
	}
	if second.ETag != "" || second.Action != ActionCreate || !strings.Contains(second.Error, "changed") && second.Error == "" {
		t.Errorf("second = %+v", second)
	}
	if !strings.Contains(second.Error, secondUID) {
		t.Errorf("error = %q, want it to name the event", second.Error)
	}
}

func TestImportProcessesEventsAfterAFailedOne(t *testing.T) {
	f := newFakeCalDAV(t, "")
	f.putStatus = map[string]int{testUID: http.StatusPreconditionFailed}

	result, err := importInvite(t.Context(), f.client, twoEvents(t), testCalendar, time.UTC)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	first, second := result.Events[0], result.Events[1]
	if !result.Written || result.Failed != 1 {
		t.Errorf("written = %v, failed = %d", result.Written, result.Failed)
	}
	if first.Error == "" || first.ETag != "" || first.Action != ActionCreate {
		t.Errorf("first = %+v", first)
	}
	if second.ETag == "" || second.Error != "" {
		t.Errorf("second = %+v", second)
	}
}

func TestImportPlanningFailureIsTreatedLikeAWriteFailure(t *testing.T) {
	f := newFakeCalDAV(t, "")
	f.reportStatus = map[string]int{secondUID: http.StatusInternalServerError}

	result, err := importInvite(t.Context(), f.client, twoEvents(t), testCalendar, time.UTC)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if !result.Written || result.Failed != 1 || result.Events[0].ETag == "" {
		t.Errorf("result = %+v", result)
	}
	if second := result.Events[1]; second.UID != secondUID || second.Error == "" || second.ETag != "" {
		t.Errorf("second = %+v", second)
	}
}

func TestImportOfASingleFailingEventIsAnError(t *testing.T) {
	f := newFakeCalDAV(t, "")
	f.putStatus = map[string]int{testUID: http.StatusPreconditionFailed}

	result, err := run(t, f, "REQUEST", "0")

	if !errors.Is(err, calendar.ErrConflict) || result != nil {
		t.Errorf("result = %+v, err = %v", result, err)
	}
}

func TestImportWhereEveryEventFailsReturnsTheFirstError(t *testing.T) {
	f := newFakeCalDAV(t, "")
	f.putStatus = map[string]int{testUID: http.StatusPreconditionFailed, secondUID: http.StatusInternalServerError}

	result, err := importInvite(t.Context(), f.client, twoEvents(t), testCalendar, time.UTC)

	if result != nil || !errors.Is(err, calendar.ErrConflict) || !strings.Contains(err.Error(), testUID) {
		t.Errorf("result = %+v, err = %v", result, err)
	}
}

func TestImportStopsAtAnEndedContext(t *testing.T) {
	t.Run("after a write", func(t *testing.T) {
		f := newFakeCalDAV(t, "")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// The context ends while the second event is being planned.
		f.afterGet = func(f *fakeCalDAV) {
			if f.lookups == 2 {
				cancel()
			}
		}

		result, err := importInvite(ctx, f.client, events(t, secondUID, "meeting-3@example.org"), testCalendar, time.UTC)
		if err != nil {
			t.Fatalf("import: %v", err)
		}

		if !result.Written || result.Failed != 2 || result.Events[0].ETag == "" {
			t.Fatalf("result = %+v", result)
		}
		for _, e := range result.Events[1:] {
			if e.ETag != "" || !strings.Contains(e.Error, context.Canceled.Error()) {
				t.Errorf("event = %+v", e)
			}
		}
		if f.lookups != 2 {
			t.Errorf("UID lookups = %d, want 2: the dead connection was tried again", f.lookups)
		}
	})
	t.Run("before any write", func(t *testing.T) {
		f := newFakeCalDAV(t, "")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		result, err := importInvite(ctx, f.client, twoEvents(t), testCalendar, time.UTC)

		if result != nil || !errors.Is(err, context.Canceled) {
			t.Errorf("result = %+v, err = %v", result, err)
		}
		if f.lookups != 0 {
			t.Errorf("UID lookups = %d, want 0", f.lookups)
		}
	})
}

func TestCleanImportHasNoErrorFields(t *testing.T) {
	f := newFakeCalDAV(t, "")

	result, err := importInvite(t.Context(), f.client, twoEvents(t), testCalendar, time.UTC)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"error"`) || strings.Contains(string(data), `"failed"`) {
		t.Errorf("json = %s", data)
	}
}

// series is a stored daily event of three days, from 10 November.
func series(sequence string) string {
	return strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN",
		"BEGIN:VEVENT", "UID:" + testUID, "DTSTAMP:20261101T120000Z", "DTSTART:20261110T120000Z",
		"DTEND:20261110T130000Z", "RRULE:FREQ=DAILY;COUNT=3", "SUMMARY:Standup", "SEQUENCE:" + sequence,
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n")
}

// cancelOf is a cancellation of single occurrences, named by their start.
func cancelOf(sequence string, starts ...string) string {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN", "METHOD:CANCEL"}
	for _, start := range starts {
		lines = append(lines, "BEGIN:VEVENT", "UID:"+testUID, "DTSTAMP:20261102T120000Z", "RECURRENCE-ID:"+start,
			"DTSTART:"+start, "DTEND:"+start[:8]+"T130000Z", "SUMMARY:Standup", "SEQUENCE:"+sequence, "STATUS:CANCELLED", "END:VEVENT")
	}
	return strings.Join(append(lines, "END:VCALENDAR", ""), "\r\n")
}

func TestImportCancelOfTheWholeEventDeletesIt(t *testing.T) {
	f := newFakeCalDAV(t, series("0"))

	result, err := runText(t, f, inviteText("CANCEL", "1"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if !result.Written || result.Events[0].Action != ActionCancel || result.Events[0].EventID != f.path() {
		t.Errorf("result = %+v", result)
	}
	if f.stored() != "" {
		t.Error("the event is still stored")
	}
	if len(f.writes) != 1 || f.writes[0] != `DELETE If-Match="v1" If-None-Match=` {
		t.Errorf("writes = %q, want one DELETE guarded by the ETag that was found", f.writes)
	}
}

func TestImportCancelOfOccurrencesKeepsTheRest(t *testing.T) {
	f := newFakeCalDAV(t, series("0"))

	result, err := runText(t, f, cancelOf("1", "20261111T120000Z"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	event := result.Events[0]
	if !result.Written || event.Action != ActionCancel || event.ETag != "v2" {
		t.Errorf("result = %+v", result)
	}
	if got := f.stored(); !strings.Contains(got, "RRULE:FREQ=DAILY;COUNT=3") || !strings.Contains(got, "EXDATE:20261111T120000Z") {
		t.Errorf("stored copy should keep the series and exclude the day:\n%s", got)
	}
	if len(f.writes) != 1 || f.writes[0] != `PUT If-Match="v1" If-None-Match=` {
		t.Errorf("writes = %q, want one PUT guarded by the ETag that was found", f.writes)
	}
}

func TestImportCancelOfSeveralOccurrencesSkipsOnesAlreadyGone(t *testing.T) {
	f := newFakeCalDAV(t, series("0"))

	// The first day is in the series. The second is past its COUNT.
	result, err := runText(t, f, cancelOf("1", "20261110T120000Z", "20261201T120000Z"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if !result.Written || result.Events[0].Action != ActionCancel {
		t.Errorf("result = %+v", result)
	}
	if got := f.stored(); !strings.Contains(got, "EXDATE:20261110T120000Z") || strings.Contains(got, "20261201") {
		t.Errorf("stored copy:\n%s", got)
	}
}

func TestImportCancelOfAnOccurrenceAlreadyGoneWritesNothing(t *testing.T) {
	f := newFakeCalDAV(t, series("0"))

	result, err := runText(t, f, cancelOf("1", "20261201T120000Z"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	event := result.Events[0]
	if result.Written || event.Action != ActionSkip || !strings.Contains(event.Reason, "already gone") {
		t.Errorf("result = %+v", result)
	}
	if len(f.writes) != 0 || f.stored() != series("0") {
		t.Errorf("writes = %q: the stored copy should not be touched", f.writes)
	}
}

func TestImportCancelOfTheLastOccurrenceDeletesTheEvent(t *testing.T) {
	// A copy holding only one changed occurrence, as an invite for one day leaves.
	only := strings.Replace(cancelOf("0", "20261111T120000Z"), "METHOD:CANCEL\r\n", "", 1)
	only = strings.Replace(only, "STATUS:CANCELLED\r\n", "", 1)
	f := newFakeCalDAV(t, only)

	result, err := runText(t, f, cancelOf("1", "20261111T120000Z"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if !result.Written || result.Events[0].Action != ActionCancel || f.stored() != "" {
		t.Errorf("result = %+v, stored = %q", result, f.stored())
	}
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "DELETE") {
		t.Errorf("writes = %q, want one DELETE", f.writes)
	}
}

func TestImportCancelOfOccurrencesDoesNotOverwriteAChangeMadeAfterTheLookup(t *testing.T) {
	f := newFakeCalDAV(t, series("0"))
	f.afterGet = func(f *fakeCalDAV) { f.etag = "v-phone" }

	_, err := runText(t, f, cancelOf("1", "20261111T120000Z"))

	if !errors.Is(err, calendar.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if strings.Contains(f.stored(), "EXDATE") {
		t.Error("the occurrence was removed over a newer copy")
	}
}

func TestImportCancelOfOccurrencesOfAnEventDeletedAfterTheLookupIsFine(t *testing.T) {
	f := newFakeCalDAV(t, series("0"))
	f.missing = http.StatusNotFound
	f.afterGet = func(f *fakeCalDAV) { f.data = "" }

	result, err := runText(t, f, cancelOf("1", "20261111T120000Z"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if result.Written || result.Events[0].Action != ActionSkip {
		t.Errorf("result = %+v", result)
	}
}

func TestImportSkipsWhatIsOlderThanTheStoredCopy(t *testing.T) {
	stored := strings.Replace(inviteText("PUBLISH", "5"), "METHOD:PUBLISH\r\n", "", 1)
	tests := []struct {
		name, method string
	}{
		{"an old invite", "REQUEST"},
		{"an old cancellation", "CANCEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeCalDAV(t, stored)

			result, err := run(t, f, tt.method, "2")
			if err != nil {
				t.Fatalf("import: %v", err)
			}

			event := result.Events[0]
			if result.Written || event.Action != ActionSkip || !strings.Contains(event.Reason, "newer") {
				t.Errorf("result = %+v", result)
			}
			if event.ExistingSequence == nil || *event.ExistingSequence != 5 {
				t.Errorf("existing sequence = %v, want 5", event.ExistingSequence)
			}
			if len(f.writes) != 0 || f.stored() != stored {
				t.Errorf("writes = %q: the stored copy should not be touched", f.writes)
			}
		})
	}
}

func TestImportCancelOfAnUnknownEventWritesNothing(t *testing.T) {
	f := newFakeCalDAV(t, "")

	result, err := run(t, f, "CANCEL", "1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if result.Written || result.Events[0].Action != ActionSkip || len(f.writes) != 0 {
		t.Errorf("result = %+v, writes = %q", result, f.writes)
	}
}

func TestPlanWarnsOfBusyTimeOnlyForNewEvents(t *testing.T) {
	busy := strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//t//t//EN", "BEGIN:VEVENT", "UID:other@example.org",
		"DTSTAMP:20261101T120000Z", "DTSTART:20261110T123000Z", "DTEND:20261110T133000Z", "SUMMARY:Dentist",
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\n")
	invite, err := ical.ParseInvite([]byte(inviteText("REQUEST", "0")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a new event that overlaps", func(t *testing.T) {
		f := newFakeCalDAV(t, "")
		f.busy = busy

		event, existing, err := planEvent(t.Context(), f.client, invite.Method, invite.Events[0], testCalendar, time.UTC)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}

		want := calendar.Interval{
			Start: time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC),
			End:   time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC),
		}
		if event.Action != ActionCreate || existing != nil {
			t.Fatalf("event = %+v, existing = %v", event, existing)
		}
		if len(event.BusyOverlap) != 1 || !event.BusyOverlap[0].Start.Equal(want.Start) || !event.BusyOverlap[0].End.Equal(want.End) {
			t.Errorf("busy overlap = %+v, want %v", event.BusyOverlap, want)
		}
	})
	t.Run("an update is not warned about", func(t *testing.T) {
		f := newFakeCalDAV(t, strings.Replace(inviteText("PUBLISH", "0"), "METHOD:PUBLISH\r\n", "", 1))
		f.busy = busy
		newer, err := ical.ParseInvite([]byte(inviteText("REQUEST", "1")), time.UTC)
		if err != nil {
			t.Fatal(err)
		}

		event, _, err := planEvent(t.Context(), f.client, newer.Method, newer.Events[0], testCalendar, time.UTC)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}

		if event.Action != ActionUpdate || len(event.BusyOverlap) != 0 {
			t.Errorf("action = %q, busy overlap = %+v", event.Action, event.BusyOverlap)
		}
	})
	t.Run("a free slot", func(t *testing.T) {
		f := newFakeCalDAV(t, "")
		f.busy = strings.NewReplacer("20261110T123000Z", "20261111T123000Z", "20261110T133000Z", "20261111T133000Z").Replace(busy)

		event, _, err := planEvent(t.Context(), f.client, invite.Method, invite.Events[0], testCalendar, time.UTC)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}

		if len(event.BusyOverlap) != 0 {
			t.Errorf("busy overlap = %+v", event.BusyOverlap)
		}
	})
}

func TestPlanFillsInWhatTheInviteSays(t *testing.T) {
	f := newFakeCalDAV(t, "")
	invite, err := ical.ParseInvite([]byte(inviteText("REQUEST", "3")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	event, _, err := planEvent(t.Context(), f.client, invite.Method, invite.Events[0], testCalendar, time.UTC)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if event.Summary != "Meeting" || event.Sequence != 3 || !event.Start.Equal(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)) ||
		!event.End.Equal(time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)) || event.ExistingSequence != nil {
		t.Errorf("event = %+v", event)
	}
}

func TestApplyWritesNothingWithoutAFoundEvent(t *testing.T) {
	f := newFakeCalDAV(t, "")
	invite, err := ical.ParseInvite([]byte(inviteText("REQUEST", "1")), time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("an update of an event that was not found", func(t *testing.T) {
		written, err := apply(t.Context(), f.client, invite, &Event{UID: testUID, Action: ActionUpdate}, nil, testCalendar, time.UTC)

		if written || !errors.Is(err, calendar.ErrEventNotFound) {
			t.Errorf("written = %v, err = %v", written, err)
		}
	})
	t.Run("a skip", func(t *testing.T) {
		written, err := apply(t.Context(), f.client, invite, &Event{UID: testUID, Action: ActionSkip}, nil, testCalendar, time.UTC)

		if written || err != nil {
			t.Errorf("written = %v, err = %v", written, err)
		}
	})
	t.Run("a cancellation with nothing stored", func(t *testing.T) {
		written, err := apply(t.Context(), f.client, invite, &Event{UID: testUID, Action: ActionCancel}, nil, testCalendar, time.UTC)

		if written || err != nil {
			t.Errorf("written = %v, err = %v", written, err)
		}
	})
	if len(f.writes) != 0 {
		t.Errorf("writes = %q", f.writes)
	}
}

func TestPreviewAndImportNeedACalendar(t *testing.T) {
	// The check comes before any mail is read, so no mailbox is needed.
	if _, err := Preview(t.Context(), nil, nil, "id", "", time.UTC); err == nil {
		t.Error("Preview accepted an empty calendar_id")
	}
	if _, err := Import(t.Context(), nil, nil, "id", "", time.UTC); err == nil {
		t.Error("Import accepted an empty calendar_id")
	}
}

func TestObjectPathCannotBeSteeredByTheUID(t *testing.T) {
	for _, uid := range []string{"../../etc/passwd", "a/b", "x\ny", "https://evil.example/"} {
		got := objectPath("/cal/work/", uid)

		name, ok := strings.CutPrefix(got, "/cal/work/")
		name, isICS := strings.CutSuffix(name, ".ics")
		if !ok || !isICS || strings.Trim(name, "0123456789abcdef") != "" {
			t.Errorf("objectPath(%q) = %q, want a hex name in the calendar", uid, got)
		}
	}
	if objectPath("/cal/work", "u") != objectPath("/cal/work/", "u") {
		t.Error("a trailing slash on the calendar changes the path")
	}
	if objectPath("/cal/work/", "a") == objectPath("/cal/work/", "b") {
		t.Error("different UIDs share a path")
	}
}
