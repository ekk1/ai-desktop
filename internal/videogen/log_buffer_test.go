package videogen

import (
	"bytes"
	"testing"
	"time"
)

func TestVideoLogBufferTracksAbsoluteOffsetsAndCopiesSnapshots(t *testing.T) {
	buffer := newVideoLogBuffer(8)
	_, _ = buffer.Write([]byte("12345"))
	_, _ = buffer.Write([]byte("67890"))

	snapshot := buffer.snapshot()
	if snapshot.CapacityBytes != 8 || snapshot.StartOffset != 2 || snapshot.EndOffset != 10 || string(snapshot.Data) != "34567890" {
		t.Fatalf("snapshot = %#v, want capacity 8, offsets [2,10), and data %q", snapshot, "34567890")
	}
	snapshot.Data[0] = 'X'
	if got := string(buffer.snapshot().Data); got != "34567890" {
		t.Fatalf("snapshot exposed retained bytes: %q", got)
	}
}

func TestVideoLogBufferSnapshotAndSubscribeShareOneBoundary(t *testing.T) {
	buffer := newVideoLogBuffer(64)
	_, _ = buffer.Write([]byte("before"))
	snapshot, chunks, cancel := buffer.subscribeWithSnapshot()
	defer cancel()
	input := []byte("after")
	_, _ = buffer.Write(input)
	input[0] = 'X'

	if snapshot.StartOffset != 0 || snapshot.EndOffset != 6 || string(snapshot.Data) != "before" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	select {
	case chunk := <-chunks:
		if chunk.Offset != snapshot.EndOffset || !bytes.Equal(chunk.Data, []byte("after")) {
			t.Fatalf("chunk = %#v after snapshot %#v", chunk, snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for log chunk")
	}
}

func TestVideoLogBufferDropsSlowSubscriberWithoutBlockingWriter(t *testing.T) {
	buffer := newVideoLogBuffer(64)
	_, chunks, cancel := buffer.subscribeWithSnapshot()
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10_000 {
			_, _ = buffer.Write([]byte("x"))
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked log writes")
	}
	for {
		select {
		case _, open := <-chunks:
			if !open {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("slow subscriber was not closed")
		}
	}
}
