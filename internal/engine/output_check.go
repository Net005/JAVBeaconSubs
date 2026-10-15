package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

// Check the real input and output directory before spending time on inference.
// A probe never opens or replaces existing user output.
func checkOutputDirectory(input string) error {
	info, err := os.Stat(input)
	if err != nil {
		return fmt.Errorf("input unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("input is not a regular media file")
	}
	f, err := os.CreateTemp(filepath.Dir(input), ".javbeaconsubs-write-check-*")
	if err != nil {
		return fmt.Errorf("output directory is not writable; check the service UID/GID and share permissions: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o664); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("output directory write check: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Remove(name)
}
