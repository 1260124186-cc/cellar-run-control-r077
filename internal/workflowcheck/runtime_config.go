package workflowcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/runtimeconfig"
)

type runtimeConfigBody struct {
	Current  runtimeConfigDocument  `json:"current"`
	Previous *runtimeConfigDocument `json:"previous"`
}

type runtimeConfigDocument struct {
	Revision              int64  `json:"revision"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	MaxRequestBodyBytes   int64  `json:"max_request_body_bytes"`
	LogLevel              string `json:"log_level"`
	Note                  string `json:"note"`
}

func (r response) runtimeConfig() (runtimeConfigBody, error) {
	var body runtimeConfigBody
	if err := r.decode(&body); err != nil {
		return runtimeConfigBody{}, err
	}
	return body, nil
}

func (r response) revisionHeader() (int64, error) {
	value := r.header.Get("X-Runtime-Config-Revision")
	if value == "" {
		return 0, fmt.Errorf("X-Runtime-Config-Revision header is missing")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("X-Runtime-Config-Revision header %q is not an integer", value)
	}
	return parsed, nil
}

func runtimeConfigPublish(api *client, dataDir string) error {
	// 1. A fresh data directory exposes the built-in default revision 1 and
	// no previous edition.
	initial, err := api.call("GET", "/v1/runtime-config", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(initial, 200, "read initial runtime configuration"); err != nil {
		return err
	}
	initialBody, err := initial.runtimeConfig()
	if err != nil {
		return err
	}
	if initialBody.Current.Revision != 1 || initialBody.Previous != nil {
		return fmt.Errorf("expected default revision 1 without a previous edition, got %+v", initialBody)
	}

	// 2. Rollback before any publication must fail without changing state.
	denied, err := api.call("POST", "/v1/runtime-config/rollback", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(denied, 409, "no_previous_config", "rollback with no previous"); err != nil {
		return err
	}

	// 3. The night shift publishes tighter parameters with optimistic
	// concurrency pinning revision 1.
	night := map[string]any{
		"request_timeout_seconds": 10,
		"max_request_body_bytes":  2048,
		"log_level":               "debug",
		"note":                    "night shift",
	}
	pinned := int64(1)
	published, err := api.call("POST", "/v1/runtime-config/publish", night, &pinned)
	if err != nil {
		return err
	}
	if err := requireStatus(published, 200, "publish night shift configuration"); err != nil {
		return err
	}
	publishedBody, err := published.runtimeConfig()
	if err != nil {
		return err
	}
	if publishedBody.Current.Revision != 2 ||
		publishedBody.Current.RequestTimeoutSeconds != 10 ||
		publishedBody.Current.MaxRequestBodyBytes != 2048 ||
		publishedBody.Current.LogLevel != "debug" {
		return fmt.Errorf("published current edition mismatch: %+v", publishedBody.Current)
	}
	if publishedBody.Previous == nil || publishedBody.Previous.Revision != 1 {
		return fmt.Errorf("expected previous revision 1 after publish, got %+v", publishedBody.Previous)
	}
	// The publishing request started while revision 1 was active, so its
	// pinned header is 1; the revision is committed for subsequent requests.
	if revision, err := published.revisionHeader(); err != nil || revision != 1 {
		return fmt.Errorf("publish response should carry the revision pinned at request start (1), got %d (%v)", revision, err)
	}

	// 4. A second supervisor still pinning revision 1 must be rejected with a
	// revision conflict, leaving revision 2 active.
	racer := map[string]any{
		"request_timeout_seconds": 30,
		"max_request_body_bytes":  4096,
		"log_level":               "warn",
		"note":                    "late publisher",
	}
	conflict, err := api.call("POST", "/v1/runtime-config/publish", racer, &pinned)
	if err != nil {
		return err
	}
	if err := requireCode(conflict, 412, "revision_conflict", "concurrent stale publish"); err != nil {
		return err
	}
	after, err := api.call("GET", "/v1/runtime-config", nil, nil)
	if err != nil {
		return err
	}
	afterBody, err := after.runtimeConfig()
	if err != nil {
		return err
	}
	if afterBody.Current.Revision != 2 || afterBody.Current.LogLevel != "debug" {
		return fmt.Errorf("failed publish must not change the active edition, got %+v", afterBody.Current)
	}

	// 5. Invalid values are rejected per field and never reach storage.
	invalid := map[string]any{
		"request_timeout_seconds": 0,
		"max_request_body_bytes":  2048,
		"log_level":               "trace",
		"note":                    "bad edition",
	}
	rejected, err := api.call("POST", "/v1/runtime-config/publish", invalid, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(rejected, 400, "publish invalid configuration"); err != nil {
		return err
	}
	if code := rejected.errorCode(); code != "invalid_input" {
		return fmt.Errorf("expected invalid_input, got %q", code)
	}

	// 6. The new body limit applies to subsequent requests: a payload larger
	// than 2048 bytes is rejected.
	big := map[string]any{
		"request_timeout_seconds": 30,
		"max_request_body_bytes":  4096,
		"log_level":               "info",
		"note":                    strings.Repeat("x", 3000),
	}
	tooLarge, err := api.call("POST", "/v1/runtime-config/publish", big, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(tooLarge, 400, "oversized publish rejected by new body limit"); err != nil {
		return err
	}

	// 7. Rollback restores the parameters of revision 1 as a new confirmed
	// revision 3, and keeps revision 2 as the preceding edition.
	rolled, err := api.call("POST", "/v1/runtime-config/rollback",
		map[string]any{"note": "supervisor rollback"}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(rolled, 200, "rollback to previous configuration"); err != nil {
		return err
	}
	rolledBody, err := rolled.runtimeConfig()
	if err != nil {
		return err
	}
	if rolledBody.Current.Revision != 3 ||
		rolledBody.Current.RequestTimeoutSeconds != 20 ||
		rolledBody.Current.MaxRequestBodyBytes != 1<<20 ||
		rolledBody.Current.LogLevel != "info" {
		return fmt.Errorf("rollback should restore revision 1 parameters, got %+v", rolledBody.Current)
	}
	if rolledBody.Previous == nil || rolledBody.Previous.Revision != 2 ||
		rolledBody.Previous.LogLevel != "debug" {
		return fmt.Errorf("rollback should retain revision 2 as previous, got %+v", rolledBody.Previous)
	}

	// 8. The committed document on disk is exactly what a restart recovers.
	if err := verifyPersistedConfig(dataDir, rolledBody); err != nil {
		return err
	}

	// 9. Reopening the store simulates a service restart: only the confirmed
	// revision is recovered; there is no half-written edition anywhere.
	reopened, err := runtimeconfig.Open(dataDir)
	if err != nil {
		return fmt.Errorf("reopen runtime configuration after restart: %w", err)
	}
	recovered := reopened.CurrentView()
	if recovered.Revision != 3 || recovered.LogLevel != runtimeconfig.LevelInfo ||
		recovered.MaxRequestBodyBytes != 1<<20 {
		return fmt.Errorf("restart recovered wrong edition: %+v", recovered)
	}
	history := reopened.History()
	if history.Previous == nil || history.Previous.Revision != 2 {
		return fmt.Errorf("restart must recover the previous revision too: %+v", history)
	}

	// 10. Stale rollback pinning is rejected just like publishing.
	stalePinned := int64(2)
	staleRollback, err := api.call("POST", "/v1/runtime-config/rollback", nil, &stalePinned)
	if err != nil {
		return err
	}
	if err := requireCode(staleRollback, 412, "revision_conflict", "stale rollback"); err != nil {
		return err
	}
	return nil
}

func verifyPersistedConfig(dataDir string, expected runtimeConfigBody) error {
	raw, err := os.ReadFile(filepath.Join(dataDir, "runtime-config.json"))
	if err != nil {
		return fmt.Errorf("read persisted runtime configuration: %w", err)
	}
	var persisted struct {
		Current  runtimeConfigDocument  `json:"current"`
		Previous *runtimeConfigDocument `json:"previous"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return fmt.Errorf("decode persisted runtime configuration: %w", err)
	}
	if persisted.Current.Revision != expected.Current.Revision ||
		persisted.Current.LogLevel != expected.Current.LogLevel ||
		persisted.Current.MaxRequestBodyBytes != expected.Current.MaxRequestBodyBytes {
		return fmt.Errorf("persisted current edition mismatch: %+v", persisted.Current)
	}
	if persisted.Previous == nil || persisted.Previous.Revision != 2 {
		return fmt.Errorf("persisted previous edition mismatch: %+v", persisted.Previous)
	}
	return nil
}
