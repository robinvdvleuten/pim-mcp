package importer

import "testing"

func TestDecide(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		incoming int
		stored   int
		exists   bool
		want     string
	}{
		{name: "new invite", method: "REQUEST", want: ActionCreate},
		{name: "new published event", method: "PUBLISH", want: ActionCreate},
		{name: "same invite again", method: "REQUEST", incoming: 2, stored: 2, exists: true, want: ActionSkip},
		{name: "newer invite", method: "REQUEST", incoming: 3, stored: 2, exists: true, want: ActionUpdate},
		{name: "older invite", method: "REQUEST", incoming: 1, stored: 2, exists: true, want: ActionSkip},
		{name: "cancellation of a known event", method: "CANCEL", incoming: 3, stored: 2, exists: true, want: ActionCancel},
		{name: "cancellation at the same sequence", method: "CANCEL", incoming: 2, stored: 2, exists: true, want: ActionCancel},
		{name: "old cancellation, the event changed since", method: "CANCEL", incoming: 1, stored: 2, exists: true, want: ActionSkip},
		{name: "cancellation of an unknown event", method: "CANCEL", want: ActionSkip},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, reason := decide(tt.method, tt.incoming, tt.stored, tt.exists)

			if action != tt.want {
				t.Errorf("action = %q (%s), want %q", action, reason, tt.want)
			}
			if reason == "" {
				t.Error("no reason given")
			}
		})
	}
}
