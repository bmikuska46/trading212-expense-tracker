package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"budget-tracking/backend/internal/database"
)

func TestStaticRoutingAndSameOriginAPI(t *testing.T) {
	directory := t.TempDir()
	key := filepath.Join(directory, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(context.Background(), filepath.Join(directory, "data", "budget.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	web := filepath.Join(directory, "web")
	for name, content := range map[string]string{
		"index.html": "overview", "accounts/index.html": "accounts", "imports/index.html": "imports",
		"_next/static/test.js": "export default 1", "accounts/index.txt": "rsc", "404.html": "not found",
	} {
		target := filepath.Join(web, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := New(store, web, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, url string
		code        int
		body        string
	}{
		{"GET", "/", 200, "overview"}, {"GET", "/accounts", 200, "accounts"},
		{"GET", "/accounts/?id=created-after-build", 200, "accounts"}, {"GET", "/imports/", 200, "imports"},
		{"GET", "/accounts/index.txt", 200, "rsc"}, {"GET", "/_next/static/test.js", 200, "export default 1"},
		{"GET", "/missing", 404, "not found"}, {"HEAD", "/missing", 404, ""}, {"POST", "/accounts", 405, "Method not allowed"},
		{"GET", "/api/missing", 404, "API endpoint not found"}, {"POST", "/api/health", 404, "API endpoint not found"},
	} {
		t.Run(test.method+test.url, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.url, nil))
			if response.Code != test.code || !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("got %d %q", response.Code, response.Body.String())
			}
			if strings.HasPrefix(test.url, "/api/") && !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatal("API fell through to HTML")
			}
			if test.url == "/_next/static/test.js" && !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
				t.Fatal("missing immutable cache policy")
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	var status Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || status.Status != "ok" || status.Database != "encrypted" || status.CipherVersion == "" {
		t.Fatalf("unexpected health: %+v", status)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("health must not be cached")
	}
	store.DB.Close()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if response.Code != 503 {
		t.Fatal("closed database reported healthy")
	}
}
