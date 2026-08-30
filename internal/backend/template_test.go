package backend

import (
	"strings"
	"testing"
)

func TestExpandCommandSupportsRawAndShellQuotedValues(t *testing.T) {
	got, err := ExpandCommand("serve ${MODEL} --label ${LABEL_SH}", map[string]string{
		"MODEL": "/models/model.gguf",
		"LABEL": "user's model",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "serve /models/model.gguf --label 'user'\\''s model'"
	if got != want {
		t.Fatalf("ExpandCommand = %q, want %q", got, want)
	}
}

func TestExpandCommandRejectsUnknownVariable(t *testing.T) {
	_, err := ExpandCommand("serve ${MODEL}", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "MODEL") {
		t.Fatalf("error = %v, want unknown MODEL", err)
	}
}

func TestExpandCommandCollapsesDoubleDollar(t *testing.T) {
	got, err := ExpandCommand("echo $$HOME", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo $HOME" {
		t.Fatalf("ExpandCommand = %q", got)
	}
}
