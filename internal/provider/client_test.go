package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func catalogClient(t *testing.T) *Client {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE registry_providers(id TEXT PRIMARY KEY,protocol TEXT,base_url TEXT,secret_ref TEXT); INSERT INTO registry_providers VALUES('synthetic','openai','http://127.0.0.1',NULL)`); err != nil {
		t.Fatal(err)
	}
	return New(db, nil)
}

func TestCatalogPagesShareOneOverallDeadline(t *testing.T) {
	client := catalogClient(t)
	client.HTTP.Timeout = 0
	var deadline time.Time
	calls := 0
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		current, ok := r.Context().Deadline()
		if !ok || time.Until(current) > 45*time.Second {
			t.Fatal("catalog has no bounded overall deadline")
		}
		if calls == 1 {
			deadline = current
		} else if !current.Equal(deadline) {
			t.Fatal("each page renewed the catalog deadline")
		}
		body := `{"data":[{"id":"second"}]}`
		if calls == 1 {
			body = `{"data":[{"id":"first"}],"nextPageToken":"next"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	models, err := client.Models(context.Background(), "synthetic")
	if err != nil || len(models) != 2 || calls != 2 {
		t.Fatalf("paged discovery %+v: %v", models, err)
	}
}

func TestCatalogTimeoutReturnsStructuredResponseWithoutPartialResults(t *testing.T) {
	client := catalogClient(t)
	calls := 0
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"partial"}],"nextPageToken":"next"}`)), Header: make(http.Header)}, nil
		}
		return nil, context.DeadlineExceeded
	})
	if models, err := client.Models(context.Background(), "synthetic"); !errors.Is(err, context.DeadlineExceeded) || models != nil {
		t.Fatalf("timeout returned partial results %+v: %v", models, err)
	}
	// The discovery route went with the model page; the catalog read itself is
	// still the one the planner and the gateway resolve models through.
	calls = 0
	if _, err := client.Models(context.Background(), "synthetic"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second timeout = %v", err)
	}
}
