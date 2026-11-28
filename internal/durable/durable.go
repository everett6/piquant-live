// Package durable writes small JSON state files so that a power cut or a hard reset cannot leave them empty or
// half-written, and keeps the previous good copy beside them.
//
// Written after the Pi outages of 2026-11-13 and 2026-11-17: a reset seconds after a write can leave a renamed file
// with no data on an SD card (the binary came back as 0 bytes on 2026-11-13). The ledgers had the same exposure:
// os.WriteFile + os.Rename without fsync, and a fresh state would then have been written over the broken file.
package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BackupPath is the previous good copy of path.
func BackupPath(path string) string { return path + ".bak" }

// WriteFile replaces path with data without assuming JSON format. Order: write a temp file via os.CreateTemp
// and fsync it; move any non-empty current file aside to path.bak (empty files go to path.corrupt-<unix nano>);
// rename the temp file to path; fsync the directory.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm, func(old []byte) bool {
		return len(old) > 0
	})
}

func writeFile(path string, data []byte, perm os.FileMode, isValidBackup func([]byte) bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		aside := BackupPath(path)
		if isValidBackup != nil && !isValidBackup(old) {
			aside = fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
		}
		if err := os.Rename(path, aside); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return syncDir(dir)
}

// WriteJSON marshals v with the given indent and writes it atomically, moving any invalid-JSON or empty
// existing file to path.corrupt-<unix nano> rather than overwriting path.bak.
func WriteJSON(path string, v any, indent string) error {
	b, err := json.MarshalIndent(v, "", indent)
	if err != nil {
		return err
	}
	return writeFile(path, b, 0o644, func(old []byte) bool {
		return len(old) > 0 && json.Valid(old)
	})
}

// ReadJSON returns the contents of path when it holds valid JSON, otherwise those of path.bak. It returns an error
// wrapping os.ErrNotExist when neither file exists, and an error naming both files when neither is usable.
// fromBackup reports which file was used.
func ReadJSON(path string) (data []byte, fromBackup bool, err error) {
	b, err := os.ReadFile(path)
	if err == nil && len(b) > 0 && json.Valid(b) {
		return b, false, nil
	}
	mainErr := err
	if mainErr == nil {
		mainErr = errors.New("empty or not valid JSON")
	}
	bb, berr := os.ReadFile(BackupPath(path))
	if berr == nil && len(bb) > 0 && json.Valid(bb) {
		return bb, true, nil
	}
	if errors.Is(mainErr, os.ErrNotExist) && errors.Is(berr, os.ErrNotExist) {
		return nil, false, fmt.Errorf("%s: %w", path, os.ErrNotExist)
	}
	return nil, false, fmt.Errorf("state file %s unusable (%v) and no usable backup (%v)", path, mainErr, berr)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
