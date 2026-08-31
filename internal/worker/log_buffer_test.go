package worker

import (
	"bytes"
	"testing"
	"time"
)

func TestLogBufferTracksAbsoluteOffsetsWhenTruncated(t *testing.T) {
	t.Parallel()
	buffer := NewLogBuffer(8)
	if _, err := buffer.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("67890")); err != nil {
		t.Fatal(err)
	}
	snapshot := buffer.Snapshot()
	if snapshot.StartOffset != 2 || snapshot.EndOffset != 10 {
		t.Fatalf("offsets = [%d,%d), want [2,10)", snapshot.StartOffset, snapshot.EndOffset)
	}
	if string(snapshot.Data) != "34567890" {
		t.Fatalf("data = %q", snapshot.Data)
	}
	snapshot.Data[0] = 'X'
	if got := string(buffer.Snapshot().Data); got != "34567890" {
		t.Fatalf("Snapshot exposed internal bytes: %q", got)
	}
}

func TestLogBufferPublishesChunksWithWriteOffsets(t *testing.T) {
	t.Parallel()
	buffer := NewLogBuffer(64)
	chunks, cancel := buffer.Subscribe()
	defer cancel()
	_, _ = buffer.Write([]byte("one"))
	_, _ = buffer.Write([]byte("two"))

	first := receiveLogChunk(t, chunks)
	second := receiveLogChunk(t, chunks)
	if first.Offset != 0 || !bytes.Equal(first.Data, []byte("one")) {
		t.Fatalf("first = %#v", first)
	}
	if second.Offset != 3 || !bytes.Equal(second.Data, []byte("two")) {
		t.Fatalf("second = %#v", second)
	}
}

func TestLogBufferDropsSlowSubscriberWithoutBlockingWriter(t *testing.T) {
	t.Parallel()
	buffer := NewLogBuffer(64)
	chunks, cancel := buffer.Subscribe()
	defer cancel()
	for index := 0; index < 1024; index++ {
		if _, err := buffer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(time.Second)
	for {
		select {
		case _, open := <-chunks:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("slow subscriber remained open")
		}
	}
}

func receiveLogChunk(t *testing.T, chunks <-chan LogChunk) LogChunk {
	t.Helper()
	select {
	case chunk, open := <-chunks:
		if !open {
			t.Fatal("log subscription closed")
		}
		return chunk
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for log chunk")
		return LogChunk{}
	}
}
