package model

// ProviderResult is the normalized outcome of querying one provider. It is the
// unit passed to the renderer and persisted in the cache. All fields are
// non-sensitive: a healthy result carries a Snapshot, a failed result carries a
// safe FailureCategory (and a Snapshot with at least Provider set for labeling).
type ProviderResult struct {
	Snapshot Snapshot        `json:"snapshot"`
	Failure  FailureCategory `json:"failure"`
}

// OK reports whether the result is a healthy snapshot (no failure).
func (r ProviderResult) OK() bool { return r.Failure == FailureNone }

// Healthy builds a successful ProviderResult from a snapshot.
func Healthy(s Snapshot) ProviderResult { return ProviderResult{Snapshot: s} }

// Failed builds a failed ProviderResult for the given provider and category.
func Failed(provider string, c FailureCategory) ProviderResult {
	return ProviderResult{Snapshot: Snapshot{Provider: provider}, Failure: c}
}
