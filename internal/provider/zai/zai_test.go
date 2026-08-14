package zai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kkamji98/aiquota/internal/model"
)

// withEnv temporarily sets environment keys for a test.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestFetchNotSignedInWhenNoKey(t *testing.T) {
	t.Setenv(envKeyPrimary, "")
	t.Setenv(envKeyLegacy, "")
	// Also neutralize the home fallback by pointing HOME at an empty dir.
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := New()
	_, err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error when no key is configured")
	}
	pe, ok := err.(*model.ProviderError)
	if !ok {
		t.Fatalf("expected *model.ProviderError, got %T", err)
	}
	if pe.Category != model.FailureNotSignedIn {
		t.Fatalf("expected FailureNotSignedIn, got %s", pe.Category)
	}
}

func TestProbeParsesServedModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"glm-5.3","choices":[]}`))
	}))
	defer srv.Close()

	orig := chatEndpoint
	chatEndpoint = srv.URL
	defer func() { chatEndpoint = orig }()

	result, category := probe(context.Background(), "test-key")
	if category != model.FailureNone {
		t.Fatalf("expected success, got %s", category)
	}
	if result.servedModel != "glm-5.3" {
		t.Fatalf("expected glm-5.3, got %q", result.servedModel)
	}
}

func TestProbeUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	orig := chatEndpoint
	chatEndpoint = srv.URL
	defer func() { chatEndpoint = orig }()

	_, category := probe(context.Background(), "bad-key")
	if category != model.FailureNotSignedIn {
		t.Fatalf("expected FailureNotSignedIn, got %s", category)
	}
}

func TestResolveKeyHomeEnvFallback(t *testing.T) {
	t.Setenv(envKeyPrimary, "")
	t.Setenv(envKeyLegacy, "")

	home := t.TempDir()
	envFile := filepath.Join(home, ".hermes", ".env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("GLM_API_KEY=homekey123\nOTHER=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	if got := resolveKey(); got != "homekey123" {
		t.Fatalf("expected homekey123, got %q", got)
	}
}

func TestResolveKeyEnvWins(t *testing.T) {
	t.Setenv(envKeyPrimary, "")
	t.Setenv(envKeyLegacy, "envkey")

	if got := resolveKey(); got != "envkey" {
		t.Fatalf("expected envkey, got %q", got)
	}
}
