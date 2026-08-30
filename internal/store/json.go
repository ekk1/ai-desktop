package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteJSON atomically replaces path with an indented JSON document. If path
// already exists, its previous contents are retained at path + ".bak".
func WriteJSON(path string, value any, perm fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create JSON directory %q: %w", dir, err)
	}

	temporary, err := os.CreateTemp(dir, ".json-*")
	if err != nil {
		return fmt.Errorf("create temporary JSON for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set permissions on temporary JSON for %q: %w", path, err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON for %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary JSON for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary JSON for %q: %w", path, err)
	}

	if err := preserveBackup(path, perm); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace JSON %q: %w", path, err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync JSON directory %q: %w", dir, err)
	}
	return nil
}

// ReadJSON decodes exactly one JSON document from path.
func ReadJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open JSON %q: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode JSON %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode JSON %q: multiple JSON values", path)
		}
		return fmt.Errorf("decode trailing JSON %q: %w", path, err)
	}
	return nil
}

func preserveBackup(path string, perm fs.FileMode) error {
	source, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open previous JSON %q: %w", path, err)
	}
	defer source.Close()

	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".backup-*")
	if err != nil {
		return fmt.Errorf("create backup for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set backup permissions for %q: %w", path, err)
	}
	if _, err := io.Copy(temporary, source); err != nil {
		return fmt.Errorf("copy backup for %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync backup for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close backup for %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path+".bak"); err != nil {
		return fmt.Errorf("replace backup for %q: %w", path, err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
