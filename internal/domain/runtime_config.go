package domain

import (
	"fmt"
	"strings"
	"time"
)

// Log levels accepted by the runtime configuration.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

const (
	minRequestTimeoutMs = 100
	maxRequestTimeoutMs = 60000
	minInFlightRequests = 1
	maxInFlightRequests = 1024
)

// RuntimeConfig is one immutable set of operational parameters. A request
// observes exactly one RuntimeConfig for its whole lifetime.
type RuntimeConfig struct {
	RequestTimeoutMs    int    `json:"request_timeout_ms"`
	LogLevel            string `json:"log_level"`
	MaxInFlightRequests int    `json:"max_in_flight_requests"`
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		RequestTimeoutMs:    5000,
		LogLevel:            LogLevelInfo,
		MaxInFlightRequests: 64,
	}
}

func (c *RuntimeConfig) Normalize() {
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
}

func (c RuntimeConfig) Validate() error {
	if c.RequestTimeoutMs < minRequestTimeoutMs || c.RequestTimeoutMs > maxRequestTimeoutMs {
		return FieldError(CodeInvalidInput, "request_timeout_ms",
			"must be between 100 and 60000")
	}
	switch c.LogLevel {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
	default:
		return FieldError(CodeInvalidInput, "log_level",
			"must be one of debug, info, warn, error")
	}
	if c.MaxInFlightRequests < minInFlightRequests || c.MaxInFlightRequests > maxInFlightRequests {
		return FieldError(CodeInvalidInput, "max_in_flight_requests",
			"must be between 1 and 1024")
	}
	return nil
}

// RuntimeConfigVersion is a committed, immutable publication of a runtime
// config. Once committed it is never modified in place.
type RuntimeConfigVersion struct {
	Version     int64         `json:"version"`
	Config      RuntimeConfig `json:"config"`
	PublishedAt time.Time     `json:"published_at"`
	PublishedBy string        `json:"published_by"`
	RollbackOf  *int64        `json:"rollback_of,omitempty"`
}

func (v RuntimeConfigVersion) Clone() RuntimeConfigVersion {
	if v.RollbackOf != nil {
		rollbackOf := *v.RollbackOf
		v.RollbackOf = &rollbackOf
	}
	return v
}

// RuntimeConfigState is the durable, append-only history of committed config
// versions. The current version is always the latest committed entry, so
// recovery can only land on a confirmed version.
type RuntimeConfigState struct {
	CurrentVersion int64                  `json:"current_version"`
	Versions       []RuntimeConfigVersion `json:"versions"`
}

func EmptyRuntimeConfigState(now time.Time) RuntimeConfigState {
	return RuntimeConfigState{
		CurrentVersion: 0,
		Versions: []RuntimeConfigVersion{{
			Version:     0,
			Config:      DefaultRuntimeConfig(),
			PublishedAt: now.UTC(),
			PublishedBy: "system",
		}},
	}
}

func (s RuntimeConfigState) Clone() RuntimeConfigState {
	cloned := RuntimeConfigState{
		CurrentVersion: s.CurrentVersion,
		Versions:       make([]RuntimeConfigVersion, len(s.Versions)),
	}
	for i, version := range s.Versions {
		cloned.Versions[i] = version.Clone()
	}
	return cloned
}

func (s RuntimeConfigState) Latest() RuntimeConfigVersion {
	return s.Versions[len(s.Versions)-1].Clone()
}

func (s RuntimeConfigState) Validate() error {
	if len(s.Versions) == 0 {
		return fmt.Errorf("runtime config must keep at least one committed version")
	}
	for i, version := range s.Versions {
		if version.Version < 0 {
			return fmt.Errorf("runtime config version cannot be negative")
		}
		if i > 0 && version.Version != s.Versions[i-1].Version+1 {
			return fmt.Errorf("runtime config versions must be contiguous")
		}
		if err := version.Config.Validate(); err != nil {
			return fmt.Errorf("runtime config version %d is invalid: %v", version.Version, err)
		}
	}
	if s.CurrentVersion != s.Versions[len(s.Versions)-1].Version {
		return fmt.Errorf("current runtime config version must reference the latest committed version")
	}
	return nil
}
