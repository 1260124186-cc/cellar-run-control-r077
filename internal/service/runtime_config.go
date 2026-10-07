package service

import (
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const maxPublishedByLength = 64

type PublishRuntimeConfigInput struct {
	ExpectedVersion *int64
	Config          domain.RuntimeConfig
	PublishedBy     string
}

type RollbackRuntimeConfigInput struct {
	ExpectedVersion *int64
	PublishedBy     string
}

// RuntimeConfigStatus describes the currently effective version and, when one
// exists, the version a rollback would restore.
type RuntimeConfigStatus struct {
	domain.RuntimeConfigVersion
	PreviousVersion *int64 `json:"previous_version"`
}

// CurrentRuntimeConfig returns the latest committed version. It never fails
// and is safe to call once per request.
func (s *Service) CurrentRuntimeConfig() domain.RuntimeConfigVersion {
	return s.configs.Current()
}

func (s *Service) RuntimeConfigStatus() RuntimeConfigStatus {
	versions := s.configs.Versions()
	status := RuntimeConfigStatus{RuntimeConfigVersion: versions[len(versions)-1]}
	if len(versions) > 1 {
		previous := versions[len(versions)-2].Version
		status.PreviousVersion = &previous
	}
	return status
}

func (s *Service) ListRuntimeConfigVersions() []domain.RuntimeConfigVersion {
	return s.configs.Versions()
}

// PublishRuntimeConfig commits a complete parameter set as the next version.
// With an expected version, a concurrent publisher that got there first
// produces a revision conflict instead of a silent overwrite.
func (s *Service) PublishRuntimeConfig(input PublishRuntimeConfigInput) (domain.RuntimeConfigVersion, error) {
	config := input.Config
	config.Normalize()
	if err := config.Validate(); err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	publishedBy, err := normalizePublishedBy(input.PublishedBy)
	if err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	var published domain.RuntimeConfigVersion
	err = s.configs.Update(input.ExpectedVersion, func(state *domain.RuntimeConfigState) error {
		published = domain.RuntimeConfigVersion{
			Version:     state.CurrentVersion + 1,
			Config:      config,
			PublishedAt: s.clock.Now(),
			PublishedBy: publishedBy,
		}
		state.Versions = append(state.Versions, published)
		state.CurrentVersion = published.Version
		return nil
	})
	if err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	return published.Clone(), nil
}

// RollbackRuntimeConfig commits the previous version's parameters as a new
// version. History stays append-only, so a rollback is itself a confirmed
// version that survives restarts.
func (s *Service) RollbackRuntimeConfig(input RollbackRuntimeConfigInput) (domain.RuntimeConfigVersion, error) {
	publishedBy, err := normalizePublishedBy(input.PublishedBy)
	if err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	var published domain.RuntimeConfigVersion
	err = s.configs.Update(input.ExpectedVersion, func(state *domain.RuntimeConfigState) error {
		if len(state.Versions) < 2 {
			return domain.NewError(domain.CodeConfigState,
				"no previous runtime config version to roll back to")
		}
		target := state.Versions[len(state.Versions)-2]
		rollbackOf := target.Version
		published = domain.RuntimeConfigVersion{
			Version:     state.CurrentVersion + 1,
			Config:      target.Config,
			PublishedAt: s.clock.Now(),
			PublishedBy: publishedBy,
			RollbackOf:  &rollbackOf,
		}
		state.Versions = append(state.Versions, published)
		state.CurrentVersion = published.Version
		return nil
	})
	if err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	return published.Clone(), nil
}

func normalizePublishedBy(value string) (string, error) {
	normalized := domain.NormalizeName(value)
	if len(normalized) > maxPublishedByLength {
		return "", domain.FieldError(domain.CodeInvalidInput, "published_by",
			"must be at most 64 characters")
	}
	return normalized, nil
}
