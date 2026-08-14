package zai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// quotaPayload is the monitor API response shape, with the two windows the
// Coding Plan actually returns.
const quotaPayload = `{"code":200,"msg":"Operation successful","data":{"limits":[` +
	`{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":12000,"currentValue":12034,` +
	`"remaining":0,"percentage":100,"nextResetTime":1786720710083},` +
	`{"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":60000,"currentValue":12034,` +
	`"remaining":47965,"percentage":20,"nextResetTime":1786886533998}],` +
	`"level":"pro"},"success":true}`

// serveQuota points quotaEndpoint at a test server for the duration of the test.
func serveQuota(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	orig := quotaEndpoint
	quotaEndpoint = srv.URL
	t.Cleanup(func() { quotaEndpoint = orig })
}

// serveChat points chatEndpoint at a test server for the duration of the test.
func serveChat(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	orig := chatEndpoint
	chatEndpoint = srv.URL
	t.Cleanup(func() { chatEndpoint = orig })
}

func TestFetchReportsQuotaWindows(t *testing.T) {
	t.Setenv(envKeyPrimary, "test-key")
	serveQuota(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(quotaPayload))
	})

	snap, err := New().Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Plan != "Coding Plan Pro" {
		t.Fatalf("expected plan %q, got %q", "Coding Plan Pro", snap.Plan)
	}
	if !snap.Session.Present || snap.Session.UsedPercent != 100 {
		t.Fatalf("unexpected session window: %+v", snap.Session)
	}
	if want := time.UnixMilli(1786720710083); !snap.Session.ResetsAt.Equal(want) {
		t.Fatalf("expected session reset %v, got %v", want, snap.Session.ResetsAt)
	}
	if !snap.Weekly.Present || snap.Weekly.UsedPercent != 20 {
		t.Fatalf("unexpected weekly window: %+v", snap.Weekly)
	}
	if want := time.UnixMilli(1786886533998); !snap.Weekly.ResetsAt.Equal(want) {
		t.Fatalf("expected weekly reset %v, got %v", want, snap.Weekly.ResetsAt)
	}
}

func TestFetchQuotaUnauthorizedDoesNotProbe(t *testing.T) {
	t.Setenv(envKeyPrimary, "bad-key")
	serveQuota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	serveChat(t, func(http.ResponseWriter, *http.Request) {
		t.Error("chat endpoint must not be called after an auth failure")
	})

	_, err := New().Fetch(context.Background())
	pe, ok := err.(*model.ProviderError)
	if !ok {
		t.Fatalf("expected *model.ProviderError, got %T", err)
	}
	if pe.Category != model.FailureNotSignedIn {
		t.Fatalf("expected FailureNotSignedIn, got %s", pe.Category)
	}
}

func TestFetchFallsBackToProbeWhenQuotaUnavailable(t *testing.T) {
	t.Setenv(envKeyPrimary, "test-key")
	serveQuota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	serveChat(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"glm-5.3","choices":[]}`))
	})

	snap, err := New().Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Session.Present || snap.Weekly.Present {
		t.Fatal("fallback path must not report quota windows")
	}
	if snap.Source != "z.ai probe: serving glm-5.3" {
		t.Fatalf("unexpected source: %q", snap.Source)
	}
}

func TestFetchQuotaEmptyLimitsIsUnsupported(t *testing.T) {
	serveQuota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"limits":[],"level":"pro"},"success":true}`))
	})

	if _, category := fetchQuota(context.Background(), "test-key"); category != model.FailureUnsupported {
		t.Fatalf("expected FailureUnsupported, got %s", category)
	}
}

func TestFetchQuotaSkipsWindowsWithoutResetTime(t *testing.T) {
	serveQuota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"limits":[` +
			`{"unit":3,"percentage":40,"nextResetTime":0},` +
			`{"unit":6,"percentage":150,"nextResetTime":1786886533998}],"level":""}}`))
	})

	result, category := fetchQuota(context.Background(), "test-key")
	if category != model.FailureNone {
		t.Fatalf("expected success, got %s", category)
	}
	if result.session.Present {
		t.Fatal("a window without a reset time must stay absent")
	}
	if result.weekly.UsedPercent != 100 {
		t.Fatalf("expected percentage clamped to 100, got %d", result.weekly.UsedPercent)
	}
	if got := planLabel(result.plan); got != "Coding Plan" {
		t.Fatalf("expected bare plan label, got %q", got)
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
