package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestStatusModesDegradeLocally(t *testing.T) {
	mock := NewMock()
	status := mock.Status(context.Background())
	if !status.Mock || status.Mode != ModeAvailable || status.Owner != OwnerOfficialHost {
		t.Fatalf("unexpected default status: %#v", status)
	}
	if !status.Capabilities.Submit || !status.Capabilities.Sessions || !status.Capabilities.Reconcile {
		t.Fatalf("available adapter missing capabilities: %#v", status)
	}
	if len(status.Degraded) != 0 {
		t.Fatalf("available adapter must not be degraded: %#v", status.Degraded)
	}

	if err := mock.SetMode(ModeChanged); err != nil {
		t.Fatalf("set changed: %v", err)
	}
	changed := mock.Status(context.Background())
	if changed.Capabilities.Submit || !changed.Capabilities.Sessions || !changed.Capabilities.Reconcile {
		t.Fatalf("changed mode must degrade submit only: %#v", changed.Capabilities)
	}
	if len(changed.Degraded) == 0 {
		t.Fatalf("changed mode must report degraded capabilities")
	}
	if sessions, err := mock.Sessions(context.Background()); err != nil || len(sessions) == 0 {
		t.Fatalf("sessions should stay readable in changed mode: %v", err)
	}

	if err := mock.SetMode(ModeUnavailable); err != nil {
		t.Fatalf("set unavailable: %v", err)
	}
	unavailable := mock.Status(context.Background())
	if unavailable.Capabilities.Submit || unavailable.Capabilities.Sessions {
		t.Fatalf("unavailable mode must disable all: %#v", unavailable.Capabilities)
	}
	if _, err := mock.Sessions(context.Background()); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if err := mock.SetMode("BROKEN"); err == nil {
		t.Fatalf("invalid mode must be refused")
	}
}

func TestSessionsAreSyntheticReferencesOwnedByMockHost(t *testing.T) {
	mock := NewMock()
	sessions, err := mock.Sessions(context.Background())
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected three synthetic sessions, got %d", len(sessions))
	}
	for _, session := range sessions {
		if session.Owner != OwnerOfficialHost {
			t.Fatalf("session must be owned by mock official host: %#v", session)
		}
		if session.Kind != "SYNTHETIC_REFERENCE" {
			t.Fatalf("session must be marked synthetic: %#v", session)
		}
		if strings.Contains(session.Title, "Kimi") || strings.Contains(session.Title, "真实") {
			t.Fatalf("session looks like real data: %#v", session)
		}
	}
}

func TestSubmitBehaviorsReturnRealErrors(t *testing.T) {
	cases := []struct {
		behavior Behavior
		want     error
	}{
		{BehaviorTimeout, ErrTimeout},
		{BehaviorEmpty, ErrEmptyReceipt},
		{BehaviorOccupied, ErrOccupied},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.behavior), func(t *testing.T) {
			mock := NewMock()
			_, err := mock.Submit(context.Background(), Submission{
				Intent: "intent-" + string(testCase.behavior), TaskID: "task-x", SessionID: "mock-session-alpha",
				Digest: strings.Repeat("a", 64), Checkpoint: 1, Behavior: testCase.behavior,
			})
			if err != testCase.want {
				t.Fatalf("expected %v, got %v", testCase.want, err)
			}
		})
	}
}

func TestSubmitPanicIsObservable(t *testing.T) {
	mock := NewMock()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatalf("panic behavior must panic for the caller to isolate")
		}
	}()
	_, _ = mock.Submit(context.Background(), Submission{
		Intent: "intent-panic", TaskID: "task-x", SessionID: "mock-session-alpha",
		Digest: strings.Repeat("a", 64), Checkpoint: 1, Behavior: BehaviorPanic,
	})
}

func TestUnknownSessionAndOwnerAreRefused(t *testing.T) {
	mock := NewMock()
	_, err := mock.Submit(context.Background(), Submission{
		Intent: "intent-missing", TaskID: "task-x", SessionID: "nope", Digest: strings.Repeat("b", 64), Checkpoint: 1,
	})
	if err != ErrSessionUnknown {
		t.Fatalf("expected ErrSessionUnknown, got %v", err)
	}
}

func TestReceiptIsDeterministicAndReconcileMatches(t *testing.T) {
	mock := NewMock()
	submission := Submission{
		Intent: "intent-alpha", TaskID: "task-alpha", SessionID: "mock-session-alpha",
		Digest: strings.Repeat("c", 64), Checkpoint: 1,
	}
	first, err := mock.Submit(context.Background(), submission)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if first.ReceiptID == "" || first.TaskID != submission.TaskID || first.SessionID != submission.SessionID || first.Digest != submission.Digest {
		t.Fatalf("receipt incomplete: %#v", first)
	}
	second, err := mock.Submit(context.Background(), submission)
	if err != nil {
		t.Fatalf("repeat submit: %v", err)
	}
	if second.ReceiptID != first.ReceiptID {
		t.Fatalf("repeat submit must be idempotent: %s != %s", second.ReceiptID, first.ReceiptID)
	}

	found, ok, err := mock.Reconcile(context.Background(), ReconcileRequest{
		Intent: submission.Intent, TaskID: submission.TaskID, SessionID: submission.SessionID,
		Digest: submission.Digest, ReceiptID: first.ReceiptID, Checkpoint: 1,
	})
	if err != nil || !ok || found.ReceiptID != first.ReceiptID {
		t.Fatalf("reconcile failed: %#v %v %v", found, ok, err)
	}
	_, ok, err = mock.Reconcile(context.Background(), ReconcileRequest{
		Intent: "unknown", TaskID: submission.TaskID, SessionID: submission.SessionID, Digest: submission.Digest,
	})
	if err != nil || ok {
		t.Fatalf("unknown intent must not reconcile: %v %v", ok, err)
	}
	_, ok, err = mock.Reconcile(context.Background(), ReconcileRequest{
		Intent: submission.Intent, TaskID: "other-task", SessionID: submission.SessionID, Digest: submission.Digest,
	})
	if err != nil || ok {
		t.Fatalf("task mismatch must not reconcile: %v %v", ok, err)
	}
}

func TestConcurrentSubmitIsSafe(t *testing.T) {
	mock := NewMock()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mock.Submit(context.Background(), Submission{
				Intent: "intent-concurrent", TaskID: "task-alpha", SessionID: "mock-session-alpha",
				Digest: strings.Repeat("d", 64), Checkpoint: 1,
			})
		}()
	}
	wg.Wait()
	receipt, ok, err := mock.Reconcile(context.Background(), ReconcileRequest{
		Intent: "intent-concurrent", TaskID: "task-alpha", SessionID: "mock-session-alpha", Digest: strings.Repeat("d", 64),
	})
	if err != nil || !ok {
		t.Fatalf("reconcile after concurrency: %v %v", ok, err)
	}
	if receipt.ReceiptID == "" {
		t.Fatalf("missing receipt")
	}
}

func TestHTTPAdapterRoutes(t *testing.T) {
	mock := NewMock()
	mux := http.NewServeMux()
	mock.Register(mux)

	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/enhance/v1/adapter", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status %d", status.Code)
	}
	var body Status
	if err := json.Unmarshal(status.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Mock || body.Mode != ModeAvailable {
		t.Fatalf("unexpected status body: %#v", body)
	}

	put := httptest.NewRecorder()
	mux.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/enhance/v1/adapter", strings.NewReader(`{"mode":"CHANGED"}`)))
	if put.Code != http.StatusOK {
		t.Fatalf("put status %d: %s", put.Code, put.Body.String())
	}

	sessions := httptest.NewRecorder()
	mux.ServeHTTP(sessions, httptest.NewRequest(http.MethodGet, "/enhance/v1/sessions", nil))
	if sessions.Code != http.StatusOK {
		t.Fatalf("sessions status %d", sessions.Code)
	}
	var listing struct {
		Items     []Session `json:"items"`
		Total     int       `json:"total"`
		Ownership string    `json:"ownership"`
		Mock      bool      `json:"mock"`
	}
	if err := json.Unmarshal(sessions.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if listing.Total != 3 || listing.Ownership != OwnerOfficialHost || !listing.Mock {
		t.Fatalf("unexpected sessions listing: %#v", listing)
	}

	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, httptest.NewRequest(http.MethodPut, "/enhance/v1/adapter", strings.NewReader(`{"mode":"NOPE"}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status %d", invalid.Code)
	}
}

func TestHTTPUnavailableSessionsReturnServiceUnavailable(t *testing.T) {
	mock := NewMock()
	if err := mock.SetMode(ModeUnavailable); err != nil {
		t.Fatalf("mode: %v", err)
	}
	mux := http.NewServeMux()
	mock.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/enhance/v1/sessions", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
}
