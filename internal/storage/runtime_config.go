package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const (
	runtimeConfigFileName     = "runtime-config.json"
	runtimeConfigTempPattern  = ".runtime-config-*.tmp"
	runtimeConfigHistoryLimit = 32
)

// RuntimeConfigStore keeps the append-only history of committed runtime
// config versions. Every accepted mutation is committed to durable storage
// before it becomes visible in memory, so a failed publish never replaces
// the previously confirmed version.
type RuntimeConfigStore struct {
	mu    sync.RWMutex
	path  string
	state domain.RuntimeConfigState
	now   func() time.Time
}

func OpenRuntimeConfig(dataDir string) (*RuntimeConfigStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store := &RuntimeConfigStore{
		path: filepath.Join(dataDir, runtimeConfigFileName),
		now:  func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// NewEphemeralRuntimeConfigStore returns a non-persisting store holding the
// built-in defaults. It exists for diagnostics and tests.
func NewEphemeralRuntimeConfigStore() *RuntimeConfigStore {
	now := func() time.Time { return time.Now().UTC() }
	return &RuntimeConfigStore{
		state: domain.EmptyRuntimeConfigState(now()),
		now:   now,
	}
}

func (s *RuntimeConfigStore) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = domain.EmptyRuntimeConfigState(s.now())
		if err := writeFileAtomic(s.path, runtimeConfigTempPattern, s.state); err != nil {
			return fmt.Errorf("persist default runtime config: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runtime config: %w", err)
	}
	var loaded domain.RuntimeConfigState
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return fmt.Errorf("decode runtime config: %w", err)
	}
	if err := loaded.Validate(); err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}
	s.state = loaded
	s.removeStaleTempFiles()
	return nil
}

// removeStaleTempFiles drops leftover temporary files from publishes that
// crashed before the atomic rename. They never became confirmed versions.
func (s *RuntimeConfigStore) removeStaleTempFiles() {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(s.path), runtimeConfigTempPattern))
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func (s *RuntimeConfigStore) Current() domain.RuntimeConfigVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Latest()
}

func (s *RuntimeConfigStore) Versions() []domain.RuntimeConfigVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := make([]domain.RuntimeConfigVersion, len(s.state.Versions))
	for i, version := range s.state.Versions {
		versions[i] = version.Clone()
	}
	return versions
}

// Update applies mutate to a candidate state and commits it. The candidate
// becomes effective only after it validates and persists; any failure leaves
// the previously confirmed version in place, in memory and on disk.
func (s *RuntimeConfigStore) Update(expected *int64, mutate func(*domain.RuntimeConfigState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if expected != nil && *expected != s.state.CurrentVersion {
		return domain.NewError(domain.CodeRevisionConflict,
			"the supplied version does not match the current runtime config version")
	}
	candidate := s.state.Clone()
	if err := mutate(&candidate); err != nil {
		return err
	}
	if len(candidate.Versions) > runtimeConfigHistoryLimit {
		candidate.Versions = candidate.Versions[len(candidate.Versions)-runtimeConfigHistoryLimit:]
	}
	if err := candidate.Validate(); err != nil {
		return domain.NewError(domain.CodeStorage,
			"runtime config state is inconsistent: "+err.Error())
	}
	if s.path != "" {
		if err := writeFileAtomic(s.path, runtimeConfigTempPattern, candidate); err != nil {
			return domain.NewError(domain.CodeStorage, err.Error())
		}
	}
	s.state = candidate
	return nil
}
