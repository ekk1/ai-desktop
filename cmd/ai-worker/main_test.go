package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("version output is empty")
	}
}

func TestRunRejectsInvalidArguments(t *testing.T) {
	tests := [][]string{
		{"--port", "0"},
		{"--port", "65536"},
		{"--unknown"},
		{"positional"},
	}
	for _, arguments := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) code = %d, want 2", arguments, code)
		}
		if stderr.Len() == 0 {
			t.Errorf("run(%q) stderr is empty", arguments)
		}
	}
}
