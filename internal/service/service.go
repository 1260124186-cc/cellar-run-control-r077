package service

import (
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/logging"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/runtimeconfig"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type Service struct {
	store           *storage.Store
	runtime         *runtimeconfig.Store
	clock           clock.Clock
	logger          *logging.Logger
	runtimeObserver func(runtimeconfig.View)
}

// New wires the use-case layer. The runtime configuration store and logger
// are optional; workflow checks and narrow callers may pass nil and receive
// a default configuration and a discarding logger.
func New(store *storage.Store, timeSource clock.Clock, runtimeStore *runtimeconfig.Store, logger *logging.Logger) *Service {
	if timeSource == nil {
		timeSource = clock.System{}
	}
	if runtimeStore == nil {
		runtimeStore = runtimeconfig.NewStore(
			runtimeconfig.DefaultDocument(1, timeSource.Now()))
	}
	if logger == nil {
		logger = logging.Discard()
	}
	return &Service{
		store:   store,
		runtime: runtimeStore,
		clock:   timeSource,
		logger:  logger,
	}
}

func (s *Service) Revision() int64 {
	return s.store.Revision()
}

// OnRuntimeConfigChange registers a callback invoked after every confirmed
// publish or rollback. The callback must return quickly; it is used to sync
// process-wide facilities such as the global log threshold.
func (s *Service) OnRuntimeConfigChange(observer func(runtimeconfig.View)) {
	s.runtimeObserver = observer
}

func (s *Service) checkExpected(current int64, expected *int64) error {
	if expected != nil && current != *expected {
		return domain.NewError(domain.CodeRevisionConflict,
			"the aggregate version does not match X-Expected-Version")
	}
	return nil
}

func generateEntityID(prefix string) (string, error) {
	return domain.NewID(prefix)
}

func generatedRunCode() (string, error) {
	value, err := domain.NewID("run")
	if err != nil {
		return "", err
	}
	suffix := strings.ToUpper(strings.TrimPrefix(value, "run_"))
	return "RUN-" + suffix, nil
}
