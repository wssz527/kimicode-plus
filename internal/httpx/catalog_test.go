package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsDevCatalogUsesBoundedLargerImportLimit(t *testing.T) {
	payload := `{"known":"` + strings.Repeat("x", 3<<20) + `"}`
	var value struct {
		Known string `json:"known"`
	}
	if !Decode(httptest.NewRecorder(), httptest.NewRequest("POST", "/enhance/v1/models/modelsdev", strings.NewReader(payload)), &value) {
		t.Fatal("saved public catalogs larger than 2 MiB must be importable")
	}
	if len(value.Known) != 3<<20 {
		t.Fatal("catalog truncated")
	}
	if BodyLimit(httptest.NewRequest("POST", "/enhance/v1/tasks", nil)) != 2<<20 {
		t.Fatal("task payload limit unexpectedly widened")
	}
}
