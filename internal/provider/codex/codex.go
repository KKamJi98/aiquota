// Package codex implements the read-only Codex quota provider.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

const (
	providerName     = "codex"
	snapshotSource   = "codex app-server"
	appServerTimeout = 2 * time.Second
	minutesPerWeek   = 7 * 24 * 60
	weeklyTolerance  = 24 * 60
)

// Provider queries the installed Codex app-server in read-only mode.
type Provider struct{}

// New returns a Codex quota provider.
func New() *Provider {
	return &Provider{}
}

// Name returns the stable provider identifier.
func (*Provider) Name() string {
	return providerName
}

// Fetch reads the current Codex quota without reading credentials or asking the
// app-server to refresh them. Child-process output is decoded only in memory
// and is never returned or logged.
func (*Provider) Fetch(ctx context.Context) (model.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, appServerTimeout)
	defer cancel()

	snapshot, category := fetch(ctx)
	if category != model.FailureNone {
		return model.Snapshot{Provider: providerName}, model.NewProviderError(category)
	}
	return snapshot, nil
}

func fetch(ctx context.Context) (model.Snapshot, model.FailureCategory) {
	if ctx.Err() != nil {
		return model.Snapshot{}, failureForContext(ctx)
	}

	cmd := exec.Command("codex", "app-server")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return model.Snapshot{}, model.FailureUnavailable
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return model.Snapshot{}, model.FailureUnavailable
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return model.Snapshot{}, failureForContext(ctx)
	}
	processDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd.Process.Pid)
		case <-processDone:
		}
	}()
	defer func() {
		close(processDone)
		killProcessGroup(cmd.Process.Pid)
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	encoder := json.NewEncoder(stdin)

	var initialized json.RawMessage
	if category := call(ctx, scanner, encoder, rpcRequest{
		Method: "initialize",
		ID:     1,
		Params: initializeParams{
			ClientInfo: clientInfo{
				Name:    "aiquota",
				Title:   "aiquota",
				Version: "0.1.0",
			},
		},
	}, &initialized); category != model.FailureNone {
		return model.Snapshot{}, category
	}

	if err := encoder.Encode(rpcNotification{Method: "initialized", Params: struct{}{}}); err != nil {
		return model.Snapshot{}, failureForContext(ctx)
	}

	var account accountReadResult
	if category := call(ctx, scanner, encoder, rpcRequest{
		Method: "account/read",
		ID:     2,
		Params: accountReadParams{RefreshToken: false},
	}, &account); category != model.FailureNone {
		return model.Snapshot{}, category
	}
	if !account.signedIn() {
		return model.Snapshot{}, model.FailureNotSignedIn
	}

	var rateLimitResult rateLimitsReadResult
	if category := call(ctx, scanner, encoder, rpcRequest{
		Method: "account/rateLimits/read",
		ID:     3,
		Params: struct{}{},
	}, &rateLimitResult); category != model.FailureNone {
		return model.Snapshot{}, category
	}

	snapshot, err := mapRateLimits(rateLimitResult.RateLimits, time.Now())
	if err != nil {
		return model.Snapshot{}, providerFailureCategory(err)
	}
	return snapshot, model.FailureNone
}

func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func call(ctx context.Context, scanner *bufio.Scanner, encoder *json.Encoder, request rpcRequest, target any) model.FailureCategory {
	if err := encoder.Encode(request); err != nil {
		return failureForContext(ctx)
	}

	for scanner.Scan() {
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			continue
		}
		if response.ID != request.ID {
			continue
		}
		if response.Error != nil || len(response.Result) == 0 {
			return model.FailureUnavailable
		}
		if err := json.Unmarshal(response.Result, target); err != nil {
			return model.FailureUnsupported
		}
		return model.FailureNone
	}

	return failureForContext(ctx)
}

func failureForContext(ctx context.Context) model.FailureCategory {
	if ctx.Err() == context.DeadlineExceeded {
		return model.FailureTimedOut
	}
	return model.FailureUnavailable
}

type rpcRequest struct {
	Method string `json:"method"`
	ID     int    `json:"id"`
	Params any    `json:"params"`
}

type rpcNotification struct {
	Method string `json:"method"`
	Params any    `json:"params"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type accountReadParams struct {
	RefreshToken bool `json:"refreshToken"`
}

type accountReadResult struct {
	Account json.RawMessage `json:"account"`
}

func (r accountReadResult) signedIn() bool {
	return len(r.Account) > 0 && !bytes.Equal(bytes.TrimSpace(r.Account), []byte("null"))
}

type rateLimitsReadResult struct {
	RateLimits *rawRateLimits `json:"rateLimits"`
}

type rawRateLimits struct {
	PlanType  *string             `json:"planType"`
	Primary   *rawRateLimitWindow `json:"primary"`
	Secondary *rawRateLimitWindow `json:"secondary"`
}

type rawRateLimitWindow struct {
	UsedPercent        *int   `json:"usedPercent"`
	WindowDurationMins *int   `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

// mapRateLimits maps a decoded, non-sensitive rate-limit response to the
// provider-neutral snapshot. The supplied time keeps the mapper deterministic
// in tests and makes it independent of process execution.
func mapRateLimits(raw *rawRateLimits, updatedAt time.Time) (model.Snapshot, error) {
	if raw == nil {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnsupported)
	}

	windows := make([]decodedWindow, 0, 2)
	for _, candidate := range []*rawRateLimitWindow{raw.Primary, raw.Secondary} {
		if candidate == nil {
			continue
		}
		window, err := decodeWindow(candidate)
		if err != nil {
			return model.Snapshot{}, err
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnsupported)
	}

	snapshot := model.Snapshot{
		Provider:  providerName,
		UpdatedAt: updatedAt,
		Source:    snapshotSource,
	}
	if raw.PlanType != nil {
		snapshot.Plan = strings.TrimSpace(*raw.PlanType)
	}

	if len(windows) == 1 {
		if isWeekly(windows[0].durationMins) {
			snapshot.Weekly = windows[0].window
		} else {
			snapshot.Session = windows[0].window
		}
		return snapshot, nil
	}

	if len(windows) != 2 || windows[0].durationMins == windows[1].durationMins {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnsupported)
	}
	shorter, longer := windows[0], windows[1]
	if shorter.durationMins > longer.durationMins {
		shorter, longer = longer, shorter
	}
	if !isWeekly(longer.durationMins) {
		return model.Snapshot{}, model.NewProviderError(model.FailureUnsupported)
	}

	snapshot.Session = shorter.window
	snapshot.Weekly = longer.window
	return snapshot, nil
}

type decodedWindow struct {
	window       model.Window
	durationMins int
}

func decodeWindow(raw *rawRateLimitWindow) (decodedWindow, error) {
	if raw.UsedPercent == nil || raw.WindowDurationMins == nil || raw.ResetsAt == nil ||
		*raw.UsedPercent < 0 || *raw.UsedPercent > 100 || *raw.WindowDurationMins <= 0 || *raw.ResetsAt <= 0 {
		return decodedWindow{}, model.NewProviderError(model.FailureUnsupported)
	}
	return decodedWindow{
		window: model.Window{
			Present:     true,
			UsedPercent: *raw.UsedPercent,
			ResetsAt:    time.Unix(*raw.ResetsAt, 0),
		},
		durationMins: *raw.WindowDurationMins,
	}, nil
}

func isWeekly(durationMins int) bool {
	return durationMins >= minutesPerWeek-weeklyTolerance && durationMins <= minutesPerWeek+weeklyTolerance
}

func providerFailureCategory(err error) model.FailureCategory {
	if providerError, ok := err.(*model.ProviderError); ok {
		return providerError.Category
	}
	return model.FailureUnsupported
}
