package instance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireRejectsSecondOwnerAndAllowsReuseAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Acquire(path); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second Acquire error = %v, want already in use", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireWritesCurrentPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(contents)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock PID = %q, want %q", got, want)
	}
}

func TestLockCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
