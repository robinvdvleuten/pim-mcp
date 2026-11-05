package ical

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// corpus lists files modelled on what real programs send. See testdata/README.md.
var corpus = []struct {
	name, start, end, user string
}{
	{"outlook-request", "2027-01-15T00:00:00Z", "2027-01-16T00:00:00Z", "UTC"},
	{"outlook-recurring", "2027-03-01T00:00:00Z", "2027-05-01T00:00:00Z", "UTC"},
	{"outlook-cancel", "2027-01-15T00:00:00Z", "2027-01-16T00:00:00Z", "UTC"},
	{"teams-meeting", "2027-07-07T00:00:00Z", "2027-07-08T00:00:00Z", "UTC"},
	{"google-invite", "2027-02-10T00:00:00Z", "2027-02-11T00:00:00Z", "UTC"},
	{"google-recurring", "2027-03-01T00:00:00Z", "2027-04-01T00:00:00Z", "UTC"},
	{"google-all-day", "2027-04-01T00:00:00Z", "2027-05-01T00:00:00Z", "Europe/Amsterdam"},
	{"apple-invite", "2027-05-20T00:00:00Z", "2027-05-21T00:00:00Z", "UTC"},
	{"zoom-invite", "2027-06-01T00:00:00Z", "2027-06-02T00:00:00Z", "UTC"},
	{"nextcloud-event", "2027-09-01T00:00:00Z", "2027-10-01T00:00:00Z", "UTC"},
	{"thunderbird-event", "2027-10-05T00:00:00Z", "2027-10-06T00:00:00Z", "UTC"},
	{"sloppy-generator", "2027-08-01T00:00:00Z", "2027-08-02T00:00:00Z", "UTC"},
	{"real-shape-bare-cr", "2027-03-01T00:00:00Z", "2027-03-02T00:00:00Z", "UTC"},
	{"real-shape-bom", "2027-03-02T00:00:00Z", "2027-03-03T00:00:00Z", "UTC"},
	{"real-shape-empty-vtimezone", "2027-03-03T00:00:00Z", "2027-03-04T00:00:00Z", "UTC"},
}

func readFixture(t *testing.T, name string) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ics", name+".ics"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data, strings.TrimSpace(string(want))
}

func marshalOccurrences(t *testing.T, occurrences []Occurrence) string {
	t.Helper()
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false) // The golden files show < and > as they are.
	if err := encoder.Encode(occurrences); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

func TestCorpusExpands(t *testing.T) {
	for _, tt := range corpus {
		t.Run(tt.name, func(t *testing.T) {
			data, want := readFixture(t, tt.name)
			cal, err := Parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			got, err := Expand(cal, date(t, tt.start), date(t, tt.end), location(t, tt.user))
			if err != nil {
				t.Fatalf("expand: %v", err)
			}

			if marshalled := marshalOccurrences(t, got); marshalled != want {
				t.Errorf("occurrences differ\ngot:\n%s\nwant:\n%s", marshalled, want)
			}
		})
	}
}

// Storing an invite mustn't change what it means.
func TestCorpusSurvivesBeingStored(t *testing.T) {
	for _, tt := range corpus {
		t.Run(tt.name, func(t *testing.T) {
			data, want := readFixture(t, tt.name)
			invite, err := ParseInvite(data, location(t, tt.user))
			if err != nil {
				t.Fatalf("parse invite: %v", err)
			}

			var got []Occurrence
			for _, event := range invite.Events {
				stored, err := invite.Storable(event.UID)
				if err != nil {
					t.Fatalf("storable: %v", err)
				}
				if strings.Contains(string(stored), "METHOD:") {
					t.Errorf("METHOD left in:\n%s", stored)
				}
				cal, err := Parse(stored)
				if err != nil {
					t.Fatalf("parse stored %s: %v", stored, err)
				}
				occurrences, err := Expand(cal, date(t, tt.start), date(t, tt.end), location(t, tt.user))
				if err != nil {
					t.Fatalf("expand stored: %v", err)
				}
				got = append(got, occurrences...)
			}

			if marshalled := marshalOccurrences(t, got); marshalled != want {
				t.Errorf("occurrences differ\ngot:\n%s\nwant:\n%s", marshalled, want)
			}
		})
	}
}

func TestCorpusKeepsWhatItDoesNotUnderstand(t *testing.T) {
	tests := map[string][]string{
		"outlook-request": {"X-MICROSOFT-CDO-BUSYSTATUS:TENTATIVE", "PARTSTAT=NEEDS-ACTION", "TRIGGER;RELATED=START:-PT15M"},
		"teams-meeting":   {"X-MICROSOFT-LOCATIONDISPLAYNAME:Microsoft Teams Meeting"},
		"apple-invite":    {"X-APPLE-RADIUS=70", "X-WR-ALARMUID:"},
		"google-invite":   {"X-GOOGLE-CONFERENCE:https://meet.google.com/abc-defg-hij", "X-NUM-GUESTS=0"},
	}

	for name, wants := range tests {
		t.Run(name, func(t *testing.T) {
			data, _ := readFixture(t, name)
			invite, err := ParseInvite(data, location(t, "UTC"))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			stored, err := invite.Storable(invite.Events[0].UID)
			if err != nil {
				t.Fatalf("storable: %v", err)
			}

			unfolded := strings.ReplaceAll(string(stored), "\r\n ", "")
			for _, want := range wants {
				if !strings.Contains(unfolded, want) {
					t.Errorf("%q was lost", want)
				}
			}
		})
	}
}

func TestCorpusIsLargeEnoughAndCoversEveryProgram(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "testdata", "ics"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 20 {
		t.Errorf("only %d fixtures", len(entries))
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	for _, program := range []string{"outlook", "teams", "google", "apple", "zoom"} {
		if !slices.ContainsFunc(names, func(name string) bool { return strings.HasPrefix(name, program) }) {
			t.Errorf("nothing from %s", program)
		}
	}
}
