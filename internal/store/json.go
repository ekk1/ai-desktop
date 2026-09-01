package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
)

var ErrInvalidJSON = errors.New("invalid JSON document")

// WriteJSON atomically replaces path with an indented JSON document. If path
// already exists, its previous contents are retained at path + ".bak".
func WriteJSON(path string, value any, perm fs.FileMode) (err error) {
	return writeJSON(path, value, perm, true)
}

func writeJSON(path string, value any, perm fs.FileMode, backup bool) (err error) {
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

	if backup {
		if err := preserveBackup(path, perm); err != nil {
			return err
		}
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
	return readJSON(path, value)
}

// ReadJSONWithBackup reads and validates the primary document. It consults
// path + ".bak" only when the primary is syntactically invalid, and restores
// the validated backup without overwriting it.
func ReadJSONWithBackup(path string, value any, perm fs.FileMode, validate func() error) error {
	primaryErr := readFreshJSON(path, value)
	if primaryErr == nil {
		if validate != nil {
			return validate()
		}
		return nil
	}
	if !errors.Is(primaryErr, ErrInvalidJSON) {
		return primaryErr
	}
	backupPath := path + ".bak"
	if err := readFreshJSON(backupPath, value); err != nil {
		return fmt.Errorf("%w; read JSON backup %q: %v", primaryErr, backupPath, err)
	}
	if validate != nil {
		if err := validate(); err != nil {
			return errors.Join(primaryErr, fmt.Errorf("validate JSON backup %q: %w", backupPath, err))
		}
	}
	if err := writeJSON(path, value, perm, false); err != nil {
		return errors.Join(primaryErr, fmt.Errorf("restore JSON backup %q: %w", backupPath, err))
	}
	return nil
}

func readFreshJSON(path string, value any) error {
	target := reflect.ValueOf(value)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("JSON target must be a non-nil pointer")
	}
	fresh := reflect.New(target.Elem().Type())
	if err := readJSON(path, fresh.Interface()); err != nil {
		return err
	}
	target.Elem().Set(fresh.Elem())
	return nil
}

func readJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open JSON %q: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return classifyJSONDocumentError(path, "decode", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: decode JSON %q: multiple JSON values", ErrInvalidJSON, path)
		}
		return classifyJSONDocumentError(path, "decode trailing", err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("decode typed JSON %q: %w", path, err)
	}
	return nil
}

func classifyJSONDocumentError(path, operation string, err error) error {
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %s JSON %q: %v", ErrInvalidJSON, operation, path, err)
	}
	return fmt.Errorf("%s JSON %q: %w", operation, path, err)
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
