package runtimeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomic encodes value to a sibling temporary file, flushes it to
// durable storage, closes the handle, and atomically renames it over the
// target. A failure at any point before the rename leaves the previously
// confirmed document untouched; the rename is the single commit point.
func writeAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	handle, err := os.CreateTemp(directory, ".runtime-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary runtime configuration: %w", err)
	}
	tempName := handle.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()

	encoder := json.NewEncoder(handle)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = handle.Close()
		return fmt.Errorf("encode runtime configuration: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("commit runtime configuration bytes: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close temporary runtime configuration: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace runtime configuration: %w", err)
	}
	removeTemp = false
	return nil
}
