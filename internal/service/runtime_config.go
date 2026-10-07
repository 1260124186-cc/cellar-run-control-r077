package service

import (
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/logging"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/runtimeconfig"
)

// PublishConfigInput carries a supervisor's requested parameter edition.
type PublishConfigInput struct {
	ExpectedRevision    *int64
	RequestTimeoutSecs  int
	MaxRequestBodyBytes int64
	LogLevel            string
	Note                string
}

// CurrentRuntimeConfig returns the active edition. The returned value is a
// point-in-time copy; requests must call this once at entry and use it for
// their whole lifetime.
func (s *Service) CurrentRuntimeConfig() runtimeconfig.View {
	return s.runtime.CurrentView()
}

// RuntimeConfigHistory returns the active edition and the preceding
// confirmed edition, when one exists.
func (s *Service) RuntimeConfigHistory() runtimeconfig.History {
	return s.runtime.History()
}

// PublishRuntimeConfig validates and durably commits a new parameter
// edition. A stale expected revision, two concurrent supervisors, or a disk
// failure leave the previously confirmed edition active.
func (s *Service) PublishRuntimeConfig(input PublishConfigInput) (runtimeconfig.Document, error) {
	document, err := s.runtime.Publish(input.ExpectedRevision, runtimeconfig.Changes{
		RequestTimeoutSeconds: input.RequestTimeoutSecs,
		MaxRequestBodyBytes:   input.MaxRequestBodyBytes,
		LogLevel:              input.LogLevel,
		Note:                  input.Note,
	})
	if err != nil {
		return runtimeconfig.Document{}, mapRuntimeConfigError(err)
	}
	s.logger.At(logging.LevelInfo).Infof(
		"runtime configuration published: revision=%d timeout=%ds body=%d level=%s",
		document.Revision, document.RequestTimeoutSeconds,
		document.MaxRequestBodyBytes, document.LogLevel)
	s.notifyRuntimeChange()
	return document, nil
}

// RollbackRuntimeConfig commits a new edition carrying the parameters of the
// immediately preceding confirmed edition.
func (s *Service) RollbackRuntimeConfig(expectedRevision *int64, note string) (runtimeconfig.Document, error) {
	document, err := s.runtime.Rollback(expectedRevision, note)
	if err != nil {
		return runtimeconfig.Document{}, mapRuntimeConfigError(err)
	}
	s.logger.At(logging.LevelInfo).Infof(
		"runtime configuration rolled back: revision=%d restored level=%s",
		document.Revision, document.LogLevel)
	s.notifyRuntimeChange()
	return document, nil
}

func (s *Service) notifyRuntimeChange() {
	if s.runtimeObserver != nil {
		s.runtimeObserver(s.runtime.CurrentView())
	}
}

func mapRuntimeConfigError(err error) error {
	if fieldErr, ok := runtimeconfig.IsFieldError(err); ok {
		return domain.FieldError(domain.CodeInvalidInput, fieldErr.Field, fieldErr.Message)
	}
	if err == runtimeconfig.ErrRevisionConflict {
		return domain.NewError(domain.CodeRevisionConflict,
			"the runtime configuration revision does not match X-Expected-Version")
	}
	if err == runtimeconfig.ErrNoPrevious {
		return domain.NewError(domain.CodeConfigNoPrevious,
			"no previous confirmed runtime configuration exists")
	}
	return domain.NewError(domain.CodeStorage, err.Error())
}
