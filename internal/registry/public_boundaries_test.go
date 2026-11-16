package registry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCatalogOversizeTimeoutRetainSnapshot(t *testing.T) {
	raw, err := os.ReadFile("testdata/public-modelsdev.json")
	if err != nil {
		t.Fatal(err)
	}
	mode := "ok"
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "large":
			io.Copy(w, io.LimitReader(strings.NewReader(strings.Repeat(" ", catalogLimit+1)), catalogLimit+1))
		case "timeout":
			<-r.Context().Done()
		default:
			w.Write(raw)
		}
	}))
	defer host.Close()
	s, _ := newTestService(t)
	first, err := s.refreshCatalog(context.Background(), host.Client(), host.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"large", "timeout"} {
		mode = m
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err = s.refreshCatalog(ctx, host.Client(), host.URL)
		cancel()
		if err == nil {
			t.Fatalf("accepted %s", m)
		}
		cached, err := s.cachedCatalog(context.Background())
		if err != nil || cached.status.SHA256 != first.SHA256 || cached.status.LastError == "" {
			t.Fatalf("lost snapshot/status %+v %v", cached.status, err)
		}
	}
}
