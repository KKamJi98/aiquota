// Package zai implements the aiquota Provider for a Z.AI GLM Coding Plan
// subscription.
//
// Z.AI exposes no stable quota API: the only authoritative place the 5-hour and
// weekly credit windows appear is the authenticated web console
// (z.ai/manage-apikey/subscription). This adapter therefore performs a minimal,
// read-only HTTP probe with an API key sourced from the standard environment
// and reports what the platform can actually answer: whether the key is valid
// and which model the Coding Plan endpoint currently serves (Z.AI silently
// routes legacy model names such as glm-5.2 to the newest GLM generation).
// Credit percentages cannot be fabricated, so windows stay absent and the card
// points at the console URL instead of guessing.
//
// Auth boundary. Unlike the claude/codex adapters, this package reads one API
// key from ZAI_API_KEY (falling back to GLM_API_KEY in the environment or in a
// ~/.hermes/.env file when present). The key is used only for an in-memory
// Authorization header on a direct HTTPS call to api.z.ai, is never logged,
// cached, printed, or persisted, and is never transmitted anywhere except
// api.z.ai. If no key is found the adapter reports FailureNotSignedIn.
package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

const (
	providerName  = "zai"
	probeTimeout  = 15 * time.Second
	probeMaxBody  = 64 << 10
	consoleURL    = "https://z.ai/manage-apikey/subscription"
	probePrompt   = "hi"
	probeMaxTok   = 1
	envKeyPrimary = "ZAI_API_KEY"
	envKeyLegacy  = "GLM_API_KEY"
)

// chatEndpoint is a var so tests can point it at a local httptest server.
var chatEndpoint = "https://api.z.ai/api/coding/paas/v4/chat/completions"

// Provider queries the Z.AI GLM Coding Plan status in a read-only manner.
type Provider struct{}

// New returns a Z.AI quota provider.
func New() *Provider { return &Provider{} }

// Name returns the stable provider identifier.
func (*Provider) Name() string { return providerName }

// ConsoleURL returns the authenticated web page that holds the authoritative
// credit windows. The renderer uses it to point the user at real numbers.
func ConsoleURL() string { return consoleURL }

type chatResponse struct {
	Model string `json:"model"`
}

// probeResult is the in-memory outcome of a single probe.
type probeResult struct {
	servedModel string
}

// Fetch validates the configured API key against the Coding Plan endpoint and
// reports the model currently served. Credit windows are not available through
// any documented API, so Snapshot windows stay absent by design; the served
// model rides in Source so no schema change is needed downstream.
func (*Provider) Fetch(ctx context.Context) (model.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	key := resolveKey()
	if key == "" {
		return model.Snapshot{Provider: providerName}, model.NewProviderError(model.FailureNotSignedIn)
	}

	result, category := probe(ctx, key)
	if category != model.FailureNone {
		return model.Snapshot{Provider: providerName}, model.NewProviderError(category)
	}

	return model.Snapshot{
		Provider:  providerName,
		Plan:      "Coding Plan",
		UpdatedAt: time.Now(),
		Source:    "z.ai probe: serving " + result.servedModel,
	}, nil
}

// resolveKey finds the API key without printing it. ZAI_API_KEY wins, then
// GLM_API_KEY from the environment, then GLM_API_KEY from ~/.hermes/.env.
func resolveKey() string {
	if v := strings.TrimSpace(os.Getenv(envKeyPrimary)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(envKeyLegacy)); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".hermes", ".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, envKeyLegacy+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, envKeyLegacy+"="))
		}
	}
	return ""
}

// probe issues a one-token chat completion and returns the model name the
// endpoint reports. The response body is decoded in memory only and is never
// logged or returned raw.
func probe(ctx context.Context, key string) (probeResult, model.FailureCategory) {
	if ctx.Err() != nil {
		return probeResult{}, model.FailureTimedOut
	}

	payload, err := json.Marshal(map[string]any{
		"model":      "glm-5.2", // legacy name; Z.AI routes to the newest GLM
		"max_tokens": probeMaxTok,
		"messages": []map[string]string{
			{"role": "user", "content": probePrompt},
		},
	})
	if err != nil {
		return probeResult{}, model.FailureUnsupported
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpoint, bytes.NewReader(payload))
	if err != nil {
		return probeResult{}, model.FailureUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return probeResult{}, model.FailureTimedOut
		}
		return probeResult{}, model.FailureUnavailable
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
	if err != nil {
		return probeResult{}, model.FailureUnavailable
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return probeResult{}, model.FailureNotSignedIn
	default:
		return probeResult{}, model.FailureUnavailable
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Model == "" {
		return probeResult{}, model.FailureUnsupported
	}
	return probeResult{servedModel: parsed.Model}, model.FailureNone
}
