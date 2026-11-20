package desktop

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestReopenSessionActivity(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		allow     bool
		busy      bool
	}{
		{"empty", `{"items":[],"has_more":false}`, true, false},
		{"idle", `{"items":[{"id":"s","busy":false,"main_turn_active":false,"pending_interaction":"none"}],"has_more":false}`, true, false},
		{"busy", `{"items":[{"id":"s","busy":true,"main_turn_active":false,"pending_interaction":"none"}],"has_more":false}`, false, true},
		{"turn", `{"items":[{"id":"s","busy":false,"main_turn_active":true,"pending_interaction":"none"}],"has_more":false}`, false, true},
		{"approval", `{"items":[{"id":"s","busy":false,"main_turn_active":false,"pending_interaction":"approval"}],"has_more":false}`, false, true},
		{"another page", `{"items":[],"has_more":true}`, false, false},
		{"unknown pagination", `{"items":[]}`, false, false},
		{"missing activity", `{"items":[{"id":"s","busy":false}],"has_more":false}`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkReopenSessions(json.RawMessage(tt.raw))
			var native *NativeError
			busy := errors.As(err, &native) && native.Code == "HOST_SESSIONS_BUSY"
			if (err == nil) != tt.allow || busy != tt.busy {
				t.Fatalf("unexpected reopen decision: %v", err)
			}
		})
	}
}
