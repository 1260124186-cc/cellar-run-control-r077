package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeSnapshot durably replaces path with the encoded snapshot.
func writeSnapshot(path string, snapshot any) error {
	return writeFileAtomic(path, ".state-*.tmp", snapshot)
}

// writeFileAtomic encodes value into a temporary file in the same directory,
// commits the bytes, and atomically renames the file over path. The previous
// content of path stays intact unless the rename succeeds.
func writeFileAtomic(path, tempPattern string, value any) error {
	directory := filepath.Dir(path)
	handle, err := os.CreateTemp(directory, tempPattern)
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
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
		return fmt.Errorf("encode state: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("commit state bytes: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	removeTemp = false
	syncDirectory(directory)
	return nil
}

// syncDirectory best-effort flushes the directory entry so the rename also
// survives an operating system crash. A process restart already sees the
// rename, so a sync failure here is not treated as a write failure.
func syncDirectory(directory string) {
	handle, err := os.Open(directory)
	if err != nil {
		return
	}
	defer handle.Close()
	_ = handle.Sync()
}
