package store

import (
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
