package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type testDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
}

func TestWriteJSONCreatesReadableFileWithRequestedPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := testDocument{SchemaVersion: 1, Name: "first"}

	if err := WriteJSON(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	var got testDocument
	if err := ReadJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("document = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("mode = %o, want 600", gotMode)
	}
}

func TestWriteJSONPreservesPreviousVersionAsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	first := testDocument{SchemaVersion: 1, Name: "first"}
	second := testDocument{SchemaVersion: 1, Name: "second"}

	if err := WriteJSON(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, second, 0o600); err != nil {
		t.Fatal(err)
	}

	var backup testDocument
	if err := ReadJSON(path+".bak", &backup); err != nil {
		t.Fatal(err)
	}
	if backup != first {
		t.Fatalf("backup = %#v, want %#v", backup, first)
	}
}

func TestReadJSONRejectsTrailingJSONValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{\"schema_version\":1}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var got testDocument
	if err := ReadJSON(path, &got); err == nil {
		t.Fatal("ReadJSON accepted a second JSON value")
	}
}

func TestReadJSONWithBackupValidatesAndRestoresCorruptPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	backup := testDocument{SchemaVersion: 1, Name: "last-good"}
	newer := testDocument{SchemaVersion: 1, Name: "newer"}
	if err := WriteJSON(path, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"name":`), 0o600); err != nil {
		t.Fatal(err)
	}

	var recovered testDocument
	err := ReadJSONWithBackup(path, &recovered, 0o600, func() error {
		if recovered.SchemaVersion != 1 || recovered.Name == "" {
			return fmt.Errorf("invalid test document")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered != backup {
		t.Fatalf("recovered = %#v, want %#v", recovered, backup)
	}
	var restored testDocument
	if err := ReadJSON(path, &restored); err != nil {
		t.Fatal(err)
	}
	if restored != backup {
		t.Fatalf("restored primary = %#v, want %#v", restored, backup)
	}
	var preserved testDocument
	if err := ReadJSON(path+".bak", &preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != backup {
		t.Fatalf("preserved backup = %#v, want %#v", preserved, backup)
	}
}

func TestReadJSONWithBackupDoesNotMaskValidUnsupportedPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WriteJSON(path, testDocument{SchemaVersion: 1, Name: "supported"}, 0o600); err != nil {
		t.Fatal(err)
	}
	unsupported := testDocument{SchemaVersion: 99, Name: "future"}
	if err := WriteJSON(path, unsupported, 0o600); err != nil {
		t.Fatal(err)
	}

	var got testDocument
	err := ReadJSONWithBackup(path, &got, 0o600, func() error {
		if got.SchemaVersion != 1 {
			return fmt.Errorf("unsupported schema %d", got.SchemaVersion)
		}
		return nil
	})
	if err == nil || got != unsupported {
		t.Fatalf("read = %#v, error = %v; want unsupported primary", got, err)
	}
}

func TestReadJSONWithBackupDoesNotClassifyMissingBackupAsMissingPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1`), 0o600); err != nil {
		t.Fatal(err)
	}

	var got testDocument
	err := ReadJSONWithBackup(path, &got, 0o600, nil)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want corruption without missing-primary classification", err)
	}
}

func TestReadJSONWithBackupDoesNotRecoverTypedDecodeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	backup := testDocument{SchemaVersion: 1, Name: "backup"}
	if err := WriteJSON(path, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path+".bak", backup, 0o600); err != nil {
		t.Fatal(err)
	}
	primary := []byte(`{"schema_version":"1","name":"typed-mismatch"}`)
	if err := os.WriteFile(path, primary, 0o600); err != nil {
		t.Fatal(err)
	}

	var got testDocument
	err := ReadJSONWithBackup(path, &got, 0o600, nil)
	if err == nil || errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("error = %v, want typed decode failure without corruption classification", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(contents, primary) {
		t.Fatalf("primary = %q, read error = %v; want unchanged %q", contents, readErr, primary)
	}
}
