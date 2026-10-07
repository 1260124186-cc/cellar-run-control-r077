// Package runtimeconfig owns the versioned operational parameter document
// that cellar supervisors can publish at runtime. The store keeps exactly
// two confirmed revisions: the current one and the one immediately before
// it. Publication is a single atomic durable commit; a failure before that
// commit leaves the previously confirmed document in place.
package runtimeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const configFileName = "runtime-config.json"

// Document is one immutable edition of the operational parameters shared by
// day and night cellar shifts.
type Document struct {
	// Revision is the monotonically increasing edition number, starting at 1.
	Revision int64 `json:"revision"`
	// RequestTimeoutSeconds bounds the lifetime of one API request.
	RequestTimeoutSeconds int `json:"request_timeout_seconds"`
	// MaxRequestBodyBytes bounds an accepted request body.
	MaxRequestBodyBytes int64 `json:"max_request_body_bytes"`
	// LogLevel is the active threshold: debug, info, warn, error.
	LogLevel string `json:"log_level"`
	// PublishedAt records when the revision was confirmed.
	PublishedAt time.Time `json:"published_at"`
	// Note is an optional supervisor-supplied label for the revision.
	Note string `json:"note"`
}

// Clone returns a value copy; the document currently holds only scalar
// fields, but the method keeps callers honest as it grows.
func (d Document) Clone() Document { return d }

// Equal compares every operational field except revision metadata.
func (d Document) Equal(other Document) bool {
	return d.RequestTimeoutSeconds == other.RequestTimeoutSeconds &&
		d.MaxRequestBodyBytes == other.MaxRequestBodyBytes &&
		d.LogLevel == other.LogLevel
}

func (d Document) validate() error {
	if d.RequestTimeoutSeconds < 1 || d.RequestTimeoutSeconds > 3600 {
		return fieldError("request_timeout_seconds",
			"must be between 1 and 3600 seconds")
	}
	if d.MaxRequestBodyBytes < 1024 || d.MaxRequestBodyBytes > 16<<20 {
		return fieldError("max_request_body_bytes",
			"must be between 1024 and 16777216 bytes")
	}
	if _, err := parseLevel(d.LogLevel); err != nil {
		return fieldError("log_level", err.Error())
	}
	return nil
}

// Changes describes the operational fields a supervisor wants to publish.
// Zero values are always supplied explicitly because every edition carries
// the full parameter set.
type Changes struct {
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	MaxRequestBodyBytes   int64  `json:"max_request_body_bytes"`
	LogLevel              string `json:"log_level"`
	Note                  string `json:"note"`
}

func (c Changes) toDocument(revision int64, publishedAt time.Time) Document {
	return Document{
		Revision:              revision,
		RequestTimeoutSeconds: c.RequestTimeoutSeconds,
		MaxRequestBodyBytes:   c.MaxRequestBodyBytes,
		LogLevel:              normalizeLevel(c.LogLevel),
		PublishedAt:           publishedAt,
		Note:                  c.Note,
	}
}

// History is the confirmed state exposed to readers: the active edition and,
// when available, the edition immediately preceding it.
type History struct {
	Current  Document  `json:"current"`
	Previous *Document `json:"previous,omitempty"`
}

// View is a point-in-time copy handed to request middleware. Handlers must
// not hold it past their request, but doing so is harmless because all
// fields are value types.
type View struct {
	Revision            int64
	RequestTimeout      time.Duration
	MaxRequestBodyBytes int64
	LogLevel            Level
	Note                string
	PublishedAt         time.Time
}

// Level mirrors the logging thresholds without importing the logging package.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

func parseLevel(value string) (Level, error) {
	level := Level(strings.ToLower(strings.TrimSpace(value)))
	switch level {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
		return level, nil
	default:
		return "", fmt.Errorf("log level must be one of debug, info, warn, error")
	}
}

func normalizeLevel(value string) string {
	level, err := parseLevel(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return string(level)
}

// FieldError marks a malformed publish payload field.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func fieldError(field, message string) error {
	return &FieldError{Field: field, Message: message}
}

// IsFieldError reports whether err is a validation failure and returns it.
func IsFieldError(err error) (*FieldError, bool) {
	var fieldErr *FieldError
	return fieldErr, errors.As(err, &fieldErr)
}

type persisted struct {
	Current  Document  `json:"current"`
	Previous *Document `json:"previous,omitempty"`
}

// Store serializes configuration commits within one process and persists
// them with the same temp-file, fsync, atomic-rename discipline as the main
// state snapshot.
type Store struct {
	mu       sync.RWMutex
	path     string
	current  Document
	previous *Document
	now      func() time.Time
}

// Open loads the confirmed configuration document from dataDir, validating
// it before serving. A damaged or partially written document fails closed
// and stops startup rather than silently reverting parameters.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store := &Store{
		path: filepath.Join(dataDir, configFileName),
		now:  func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// NewStore is the in-memory constructor used by workflow checks.
func NewStore(initial Document) *Store {
	return &Store{current: initial, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.current = DefaultDocument(1, s.now())
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runtime configuration: %w", err)
	}
	var loaded persisted
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return fmt.Errorf("decode runtime configuration: %w", err)
	}
	if err := loaded.Current.validate(); err != nil {
		return fmt.Errorf("runtime configuration current revision is invalid: %w", err)
	}
	if loaded.Current.Revision < 1 {
		return fmt.Errorf("runtime configuration revision must be positive")
	}
	if loaded.Previous != nil {
		if err := loaded.Previous.validate(); err != nil {
			return fmt.Errorf("runtime configuration previous revision is invalid: %w", err)
		}
		if loaded.Previous.Revision != loaded.Current.Revision-1 {
			return fmt.Errorf("runtime configuration previous revision %d must immediately precede current %d",
				loaded.Previous.Revision, loaded.Current.Revision)
		}
	}
	s.current = loaded.Current
	s.previous = loaded.Previous
	return nil
}

// DefaultDocument returns the built-in operational parameters.
func DefaultDocument(revision int64, publishedAt time.Time) Document {
	return Document{
		Revision:              revision,
		RequestTimeoutSeconds: 20,
		MaxRequestBodyBytes:   1 << 20,
		LogLevel:              string(LevelInfo),
		PublishedAt:           publishedAt,
		Note:                  "built-in defaults",
	}
}

// CurrentView returns an immutable snapshot of the active edition. Requests
// pin this view at entry, guaranteeing the whole request observes one
// revision even if a publish commits while it runs.
func (s *Store) CurrentView() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return viewOf(s.current)
}

func viewOf(document Document) View {
	level, _ := parseLevel(document.LogLevel)
	return View{
		Revision:            document.Revision,
		RequestTimeout:      time.Duration(document.RequestTimeoutSeconds) * time.Second,
		MaxRequestBodyBytes: document.MaxRequestBodyBytes,
		LogLevel:            level,
		Note:                document.Note,
		PublishedAt:         document.PublishedAt,
	}
}

// History returns the active and preceding confirmed editions as defensive
// copies.
func (s *Store) History() History {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history := History{Current: s.current.Clone()}
	if s.previous != nil {
		previous := s.previous.Clone()
		history.Previous = &previous
	}
	return history
}

// Publish validates and commits a new edition. expectedRevision, when
// non-nil, must equal the active revision (optimistic concurrency for two
// supervisors publishing at once). The previous confirmed edition is
// retained; on any persistence failure neither edition changes in memory or
// on disk.
func (s *Store) Publish(expectedRevision *int64, changes Changes) (Document, error) {
	candidate := changes.toDocument(0, s.now())
	if err := candidate.validate(); err != nil {
		return Document{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if expectedRevision != nil && *expectedRevision != s.current.Revision {
		return Document{}, ErrRevisionConflict
	}
	previous := s.current.Clone()
	candidate.Revision = previous.Revision + 1
	// Identical parameters do not displace the rollback target: the new
	// edition is still a confirmed revision with its own receipt, but
	// rollback continues to restore the last *different* parameters.
	var nextPrevious *Document
	if candidate.Equal(previous) {
		nextPrevious = s.previous
	} else {
		nextPrevious = &previous
	}
	if err := writeAtomic(s.path, persisted{Current: candidate, Previous: nextPrevious}); err != nil {
		return Document{}, err
	}
	s.current = candidate
	s.previous = nextPrevious
	return candidate.Clone(), nil
}

// Rollback commits a new edition whose parameters are copied from the
// immediately preceding confirmed edition. It fails with ErrNoPrevious when
// there is nothing to roll back to, and with ErrRevisionConflict on a stale
// expected revision. After rollback the prior current becomes the new
// previous slot, so supervisors can toggle between two editions.
func (s *Store) Rollback(expectedRevision *int64, note string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if expectedRevision != nil && *expectedRevision != s.current.Revision {
		return Document{}, ErrRevisionConflict
	}
	if s.previous == nil {
		return Document{}, ErrNoPrevious
	}
	candidate := Document{
		Revision:              s.current.Revision + 1,
		RequestTimeoutSeconds: s.previous.RequestTimeoutSeconds,
		MaxRequestBodyBytes:   s.previous.MaxRequestBodyBytes,
		LogLevel:              s.previous.LogLevel,
		PublishedAt:           s.now(),
		Note:                  note,
	}
	if err := candidate.validate(); err != nil {
		return Document{}, err
	}
	previousCurrent := s.current.Clone()
	next := persisted{Current: candidate, Previous: &previousCurrent}
	if err := writeAtomic(s.path, next); err != nil {
		return Document{}, err
	}
	s.current = candidate
	s.previous = next.Previous
	return candidate.Clone(), nil
}

// Errors returned by publish and rollback operations.
var (
	// ErrRevisionConflict means the supplied expected revision is stale.
	ErrRevisionConflict = errors.New("runtime configuration revision conflict")
	// ErrNoPrevious means no earlier confirmed edition exists.
	ErrNoPrevious = errors.New("no previous runtime configuration to roll back to")
)
