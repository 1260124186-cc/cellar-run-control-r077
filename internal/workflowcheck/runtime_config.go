package workflowcheck

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/httpapi"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type runtimeConfigValues struct {
	RequestTimeoutMs    int    `json:"request_timeout_ms"`
	LogLevel            string `json:"log_level"`
	MaxInFlightRequests int    `json:"max_in_flight_requests"`
}

type runtimeConfigView struct {
	Version         int64               `json:"version"`
	Config          runtimeConfigValues `json:"config"`
	PublishedBy     string              `json:"published_by"`
	RollbackOf      *int64              `json:"rollback_of"`
	PreviousVersion *int64              `json:"previous_version"`
}

type runtimeConfigVersionsView struct {
	Versions []runtimeConfigView `json:"versions"`
}

func runtimeConfigPayload(timeoutMs int, level string, maxInFlight int) map[string]any {
	return map[string]any{
		"config": map[string]any{
			"request_timeout_ms":     timeoutMs,
			"log_level":              level,
			"max_in_flight_requests": maxInFlight,
		},
		"published_by": "night-lead",
	}
}

func requireRuntimeConfig(view runtimeConfigView, version int64,
	timeoutMs int, level string, maxInFlight int) error {
	if view.Version != version {
		return fmt.Errorf("expected runtime config version %d, got %d", version, view.Version)
	}
	if view.Config.RequestTimeoutMs != timeoutMs ||
		view.Config.LogLevel != level ||
		view.Config.MaxInFlightRequests != maxInFlight {
		return fmt.Errorf("runtime config version %d carries unexpected parameters %+v",
			version, view.Config)
	}
	return nil
}

func getRuntimeConfig(api *client, operation string) (runtimeConfigView, response, error) {
	resp, err := api.call("GET", "/v1/runtime-config", nil, nil)
	if err != nil {
		return runtimeConfigView{}, response{}, err
	}
	if err := requireStatus(resp, 200, operation); err != nil {
		return runtimeConfigView{}, response{}, err
	}
	var view runtimeConfigView
	if err := resp.decode(&view); err != nil {
		return runtimeConfigView{}, response{}, err
	}
	return view, resp, nil
}

func runtimeConfigPublishing(api *client, dataDir string) error {
	// A fresh data directory starts on the built-in default version 0.
	view, resp, err := getRuntimeConfig(api, "read default runtime config")
	if err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 0, 5000, domain.LogLevelInfo, 64); err != nil {
		return err
	}
	if view.PreviousVersion != nil {
		return fmt.Errorf("default runtime config must not offer a rollback target")
	}
	if resp.headerValue("X-Runtime-Config-Version") != "0" {
		return fmt.Errorf("response does not report the pinned config version")
	}

	// Publish a complete parameter set as version 1.
	published, err := api.call("POST", "/v1/runtime-config/publish",
		runtimeConfigPayload(1500, domain.LogLevelDebug, 8), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(published, 201, "publish runtime config"); err != nil {
		return err
	}
	if err := published.decode(&view); err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 1, 1500, domain.LogLevelDebug, 8); err != nil {
		return err
	}

	view, resp, err = getRuntimeConfig(api, "read published runtime config")
	if err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 1, 1500, domain.LogLevelDebug, 8); err != nil {
		return err
	}
	if view.PreviousVersion == nil || *view.PreviousVersion != 0 {
		return fmt.Errorf("published config must offer version 0 as rollback target")
	}
	if resp.headerValue("X-Runtime-Config-Version") != "1" {
		return fmt.Errorf("response does not report the pinned config version 1")
	}

	// Invalid parameter sets are rejected without creating a version.
	invalid, err := api.call("POST", "/v1/runtime-config/publish",
		runtimeConfigPayload(1500, "verbose", 8), nil)
	if err != nil {
		return err
	}
	if err := requireCode(invalid, 400, string(domain.CodeInvalidInput),
		"publish invalid log level"); err != nil {
		return err
	}

	// A stale expected version is rejected instead of overwriting.
	stale, err := api.call("POST", "/v1/runtime-config/publish",
		runtimeConfigPayload(1800, domain.LogLevelInfo, 16), ptrInt64(0))
	if err != nil {
		return err
	}
	if err := requireCode(stale, 412, string(domain.CodeRevisionConflict),
		"publish with stale expected version"); err != nil {
		return err
	}

	// Two supervisors publishing against the same version: exactly one wins.
	type publishOutcome struct {
		resp response
		err  error
	}
	outcomes := make(chan publishOutcome, 2)
	for _, timeoutMs := range []int{2000, 3000} {
		go func(timeoutMs int) {
			resp, err := api.call("POST", "/v1/runtime-config/publish",
				runtimeConfigPayload(timeoutMs, domain.LogLevelInfo, 16), ptrInt64(1))
			outcomes <- publishOutcome{resp: resp, err: err}
		}(timeoutMs)
	}
	successes := 0
	conflicts := 0
	winnerTimeout := 0
	for i := 0; i < 2; i++ {
		outcome := <-outcomes
		if outcome.err != nil {
			return outcome.err
		}
		switch outcome.resp.status {
		case 201:
			successes++
			if err := outcome.resp.decode(&view); err != nil {
				return err
			}
			if view.Version != 2 {
				return fmt.Errorf("winning publish has version %d, expected 2", view.Version)
			}
			winnerTimeout = view.Config.RequestTimeoutMs
		case 412:
			conflicts++
			if outcome.resp.errorCode() != string(domain.CodeRevisionConflict) {
				return fmt.Errorf("losing publish returned %q, expected revision_conflict",
					outcome.resp.errorCode())
			}
		default:
			return fmt.Errorf("concurrent publish returned unexpected status %d",
				outcome.resp.status)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent publish: expected one winner and one conflict, got %d and %d",
			successes, conflicts)
	}
	view, _, err = getRuntimeConfig(api, "read runtime config after concurrent publish")
	if err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 2, winnerTimeout, domain.LogLevelInfo, 16); err != nil {
		return err
	}

	// Rollback commits the previous version's parameters as a new version.
	rolledBack, err := api.call("POST", "/v1/runtime-config/rollback",
		map[string]any{"published_by": "day-lead"}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(rolledBack, 200, "roll back runtime config"); err != nil {
		return err
	}
	if err := rolledBack.decode(&view); err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 3, 1500, domain.LogLevelDebug, 8); err != nil {
		return err
	}
	if view.RollbackOf == nil || *view.RollbackOf != 1 {
		return fmt.Errorf("rollback must record version 1 as its source")
	}
	if view.PublishedBy != "day-lead" {
		return fmt.Errorf("rollback must record its publisher")
	}

	// A second rollback without a body restores the version 2 parameters.
	rolledBack, err = api.call("POST", "/v1/runtime-config/rollback", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(rolledBack, 200, "roll back runtime config again"); err != nil {
		return err
	}
	if err := rolledBack.decode(&view); err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 4, winnerTimeout, domain.LogLevelInfo, 16); err != nil {
		return err
	}

	// The committed history is append-only and contiguous.
	listed, err := api.call("GET", "/v1/runtime-config/versions", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listed, 200, "list runtime config versions"); err != nil {
		return err
	}
	var versions runtimeConfigVersionsView
	if err := listed.decode(&versions); err != nil {
		return err
	}
	if len(versions.Versions) != 5 {
		return fmt.Errorf("expected 5 committed versions, got %d", len(versions.Versions))
	}
	for i, committed := range versions.Versions {
		if committed.Version != int64(i) {
			return fmt.Errorf("committed history is not contiguous at position %d", i)
		}
	}

	// A publish that fails halfway through persistence must not replace the
	// confirmed version. Renaming onto a directory fails on the rename step.
	configPath := filepath.Join(dataDir, "runtime-config.json")
	confirmed, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read confirmed runtime config: %w", err)
	}
	if err := os.Remove(configPath); err != nil {
		return fmt.Errorf("remove runtime config for failure injection: %w", err)
	}
	if err := os.Mkdir(configPath, 0o755); err != nil {
		return fmt.Errorf("replace runtime config with directory: %w", err)
	}
	failed, err := api.call("POST", "/v1/runtime-config/publish",
		runtimeConfigPayload(999, domain.LogLevelError, 1), nil)
	if err != nil {
		return err
	}
	if err := requireCode(failed, 500, string(domain.CodeStorage),
		"publish with failing rename"); err != nil {
		return err
	}
	if err := os.Remove(configPath); err != nil {
		return fmt.Errorf("clear injected directory: %w", err)
	}
	if err := os.WriteFile(configPath, confirmed, 0o644); err != nil {
		return fmt.Errorf("restore confirmed runtime config: %w", err)
	}
	view, resp, err = getRuntimeConfig(api, "read runtime config after failed publish")
	if err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 4, winnerTimeout, domain.LogLevelInfo, 16); err != nil {
		return fmt.Errorf("failed publish replaced the confirmed version: %w", err)
	}
	if resp.headerValue("X-Runtime-Config-Version") != "4" {
		return fmt.Errorf("failed publish changed the pinned config version")
	}

	// A restart recovers the last confirmed version, never the failed publish.
	restartStore, err := storage.Open(dataDir)
	if err != nil {
		return err
	}
	restartConfigs, err := storage.OpenRuntimeConfig(dataDir)
	if err != nil {
		return err
	}
	restartServer := httptest.NewServer(httpapi.NewHandler(
		service.New(restartStore, clock.System{}, restartConfigs)))
	defer restartServer.Close()
	restartAPI := newClient(restartServer.URL)
	view, _, err = getRuntimeConfig(restartAPI, "read runtime config after restart")
	if err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 4, winnerTimeout, domain.LogLevelInfo, 16); err != nil {
		return fmt.Errorf("restart did not recover the confirmed version: %w", err)
	}

	// Publishing still works after the failed attempt.
	final, err := api.call("POST", "/v1/runtime-config/publish",
		runtimeConfigPayload(4500, domain.LogLevelWarn, 32), ptrInt64(4))
	if err != nil {
		return err
	}
	if err := requireStatus(final, 201, "publish after failed publish"); err != nil {
		return err
	}
	if err := final.decode(&view); err != nil {
		return err
	}
	if err := requireRuntimeConfig(view, 5, 4500, domain.LogLevelWarn, 32); err != nil {
		return err
	}

	health, err := api.call("GET", "/healthz", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(health, 200, "read health"); err != nil {
		return err
	}
	var healthView struct {
		RuntimeConfigVersion int64 `json:"runtime_config_version"`
	}
	if err := health.decode(&healthView); err != nil {
		return err
	}
	if healthView.RuntimeConfigVersion != 5 {
		return fmt.Errorf("health reports runtime config version %d, expected 5",
			healthView.RuntimeConfigVersion)
	}
	return nil
}
