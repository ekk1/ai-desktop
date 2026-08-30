package backend

import (
	"bytes"
	"testing"
	"time"
)

func TestLogBufferPreservesLatestRawBytes(t *testing.T) {
	buffer := NewLogBuffer(8)
	if _, err := buffer.Write([]byte("abc\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("defgh")); err != nil {
		t.Fatal(err)
	}
	if got, want := string(buffer.Snapshot()), "bc\ndefgh"; got != want {
		t.Fatalf("Snapshot = %q, want %q", got, want)
	}
	buffer.Clear()
	if got := buffer.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot after Clear = %q", got)
	}
}

func TestLogBufferSubscriptionReceivesCopiedChunksAndCloses(t *testing.T) {
	buffer := NewLogBuffer(64)
	chunks, cancel := buffer.Subscribe()
	input := []byte("server ready\n")
	if _, err := buffer.Write(input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'

	select {
	case got := <-chunks:
		if !bytes.Equal(got, []byte("server ready\n")) {
			t.Fatalf("chunk = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive a chunk")
	}

	cancel()
	if _, ok := <-chunks; ok {
		t.Fatal("subscription channel remained open after cancel")
	}
}

func TestLogBufferSlowSubscriberNeverBlocksWriter(t *testing.T) {
	buffer := NewLogBuffer(128)
	_, cancel := buffer.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10000 {
			_, _ = buffer.Write([]byte("x"))
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked log writes")
	}
}
