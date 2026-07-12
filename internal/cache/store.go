// Package cache persists a short-lived snapshot of normalized provider results
// so that a plain `aiquota` invocation can render instantly without querying the
// native CLIs. Only non-sensitive fields are stored; never raw child output,
// credentials, or account identifiers.
package cache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kkamji98/aiquota/internal/model"
)

// FreshFor is the maximum cache age at which a plain read is served from cache.
const FreshFor = 20 * time.Second

// payload is the on-disk cache shape. SavedAt drives the freshness check and the
// "updated Ns ago" label.
type payload struct {
	SavedAt time.Time              `json:"saved_at"`
	Results []model.ProviderResult `json:"results"`
}

// Store reads and writes the cache file at Path.
type Store struct {
	Path string
}

// Default returns a Store under the OS user cache directory
// (e.g. ~/Library/Caches/aiquota/cache.json on macOS), never inside the project
// or any credential directory.
func Default() (*Store, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "aiquota", "cache.json")}, nil
}

// Data is a loaded cache: the results and when they were saved.
type Data struct {
	SavedAt time.Time
	Results []model.ProviderResult
}

// Age returns how old the cached data is relative to now.
func (d Data) Age(now time.Time) time.Duration { return now.Sub(d.SavedAt) }

// Fresh reports whether the cached data is within FreshFor of now.
func (d Data) Fresh(now time.Time) bool {
	age := d.Age(now)
	return age >= 0 && age <= FreshFor
}

// Load reads and decodes the cache. It returns (nil, nil)-equivalent empty Data
// with a false ok when the cache is absent. A corrupt cache is treated as a miss.
func (s *Store) Load() (Data, bool, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Data{}, false, nil
	}
	if err != nil {
		return Data{}, false, err
	}
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		// Treat an unreadable cache as a miss rather than a hard error.
		return Data{}, false, nil
	}
	return Data{SavedAt: p.SavedAt, Results: p.Results}, true, nil
}

// Save atomically writes results with the given timestamp. It writes to a temp
// file in the same directory and renames, so a reader never sees a partial file.
func (s *Store) Save(results []model.ProviderResult, savedAt time.Time) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(payload{SavedAt: savedAt, Results: results}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

// Merge combines prior cached results with fresh ones, replacing only providers
// that succeeded this run. A provider that failed this run keeps its prior cached
// entry if one exists; otherwise the fresh failure is recorded. Provider order
// follows `order`.
func Merge(prior []model.ProviderResult, fresh []model.ProviderResult, order []string) []model.ProviderResult {
	priorByName := indexByProvider(prior)
	freshByName := indexByProvider(fresh)

	out := make([]model.ProviderResult, 0, len(order))
	for _, name := range order {
		f, hasFresh := freshByName[name]
		switch {
		case hasFresh && f.OK():
			out = append(out, f)
		default:
			if p, ok := priorByName[name]; ok {
				out = append(out, p)
			} else if hasFresh {
				out = append(out, f)
			}
		}
	}
	return out
}

func indexByProvider(rs []model.ProviderResult) map[string]model.ProviderResult {
	m := make(map[string]model.ProviderResult, len(rs))
	for _, r := range rs {
		m[r.Snapshot.Provider] = r
	}
	return m
}
