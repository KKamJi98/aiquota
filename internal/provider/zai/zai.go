// Package zai implements the aiquota Provider for a Z.AI GLM Coding Plan
// subscription.
//
// Quota source. The OpenAI-compatible Coding Plan endpoint exposes nothing
// usable: no rate-limit response headers, and /v4/usage, /v4/quota and
// /v4/limits all 404. The credit windows live on a separate monitor API,
// /api/monitor/usage/quota/limit, which the Z.AI console and the community
// trackers read. That endpoint is undocumented, so this adapter treats it as
// best-effort: when it answers with a usable payload the 5-hour and weekly
// windows are reported verbatim, and when it does not the adapter falls back to
// a minimal one-token chat probe that reports only whether the key is valid and
// which model the endpoint currently serves (Z.AI silently routes legacy model
// names such as glm-5.2 to the newest GLM generation). Credit percentages are
// never fabricated: on the fallback path the windows stay absent and the card
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

// unitHour is the calendar-field code the monitor API uses for hour-based
// windows, which is how the 5-hour session pool is reported. The remaining
// codes are undocumented; every other window this plan returns is the wider
// weekly credit pool, so anything that is not hour-based maps to Weekly.
const unitHour = 3

// Endpoints are vars so tests can point them at a local httptest server.
var (
	chatEndpoint  = "https://api.z.ai/api/coding/paas/v4/chat/completions"
	quotaEndpoint = "https://api.z.ai/api/monitor/usage/quota/limit"
)

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

// quotaResponse mirrors the monitor API payload. Only the fields the adapter
// needs are decoded: the card shows percentages and reset times, and usage is
// read solely as the window-size check that tells a real limit from an empty
// entry. Consumed and remaining balances are deliberately dropped.
type quotaResponse struct {
	Data struct {
		Level  string `json:"level"`
		Limits []struct {
			Unit          int   `json:"unit"`
			Usage         int64 `json:"usage"`
			Percentage    int   `json:"percentage"`
			NextResetTime int64 `json:"nextResetTime"`
		} `json:"limits"`
	} `json:"data"`
}

// probeResult is the in-memory outcome of a single probe.
type probeResult struct {
	servedModel string
}

// quotaResult is the in-memory outcome of a single monitor API call.
type quotaResult struct {
	plan    string
	session model.Window
	weekly  model.Window
}

// Fetch reports the Coding Plan credit windows from the monitor API. If that
// undocumented endpoint cannot answer, it degrades to a one-token chat probe
// that validates the key and names the served model; on that path the windows
// stay absent and the served model rides in Source, so the renderer shows the
// status note instead of invented percentages.
func (*Provider) Fetch(ctx context.Context) (model.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	key := resolveKey()
	if key == "" {
		return model.Snapshot{Provider: providerName}, model.NewProviderError(model.FailureNotSignedIn)
	}

	quota, category := fetchQuota(ctx, key)
	switch category {
	case model.FailureNone:
		return model.Snapshot{
			Provider:  providerName,
			Plan:      planLabel(quota.plan),
			Session:   quota.session,
			Weekly:    quota.weekly,
			UpdatedAt: time.Now(),
			Source:    "z.ai monitor api",
		}, nil
	case model.FailureNotSignedIn:
		// A rejected key will not fare better on the chat endpoint.
		return model.Snapshot{Provider: providerName}, model.NewProviderError(category)
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

// planLabel renders the subscription tier the monitor API reports, e.g. the
// "pro" level becomes "Coding Plan Pro".
func planLabel(level string) string {
	if level == "" {
		return "Coding Plan"
	}
	return "Coding Plan " + strings.ToUpper(level[:1]) + level[1:]
}

// fetchQuota reads the credit windows from the monitor API. A payload that
// carries no usable window is reported as FailureUnsupported so the caller can
// fall back rather than render an empty card.
func fetchQuota(ctx context.Context, key string) (quotaResult, model.FailureCategory) {
	body, category := get(ctx, quotaEndpoint, key)
	if category != model.FailureNone {
		return quotaResult{}, category
	}

	var parsed quotaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return quotaResult{}, model.FailureUnsupported
	}

	result := quotaResult{plan: parsed.Data.Level}
	for _, limit := range parsed.Data.Limits {
		if limit.Usage <= 0 {
			continue
		}
		// A window untouched since its last reset carries no nextResetTime:
		// the countdown starts when credits are first spent. The window is
		// real, so report it and leave the reset time absent.
		window := model.Window{
			Present:     true,
			UsedPercent: clampPercent(limit.Percentage),
		}
		if limit.NextResetTime > 0 {
			window.ResetsAt = time.UnixMilli(limit.NextResetTime)
		}
		if limit.Unit == unitHour {
			result.session = window
		} else {
			result.weekly = window
		}
	}
	if !result.session.Present && !result.weekly.Present {
		return quotaResult{}, model.FailureUnsupported
	}
	return result, model.FailureNone
}

// clampPercent keeps a provider-reported percentage inside 0-100. Z.AI can
// report over 100 when a window is overdrawn, which would render a negative bar.
func clampPercent(p int) int {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return p
	}
}

// get issues an authenticated read-only GET and returns the response body. The
// body is held in memory only and is never logged or returned raw on failure.
func get(ctx context.Context, url, key string) ([]byte, model.FailureCategory) {
	if ctx.Err() != nil {
		return nil, model.FailureTimedOut
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, model.FailureUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, model.FailureTimedOut
		}
		return nil, model.FailureUnavailable
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
	if err != nil {
		return nil, model.FailureUnavailable
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, model.FailureNone
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, model.FailureNotSignedIn
	default:
		return nil, model.FailureUnavailable
	}
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
