package videogen

import "sync"

const videoLogSubscriberCapacity = 64

// VideoLogSnapshot is a retained window in an attempt's absolute log stream.
type VideoLogSnapshot struct {
	CapacityBytes int
	StartOffset   int64
	EndOffset     int64
	Data          []byte
}

// VideoLogChunk is one raw write and its absolute stream offset.
type VideoLogChunk struct {
	Offset int64
	Data   []byte
}

type videoLogBuffer struct {
	mu          sync.Mutex
	capacity    int
	data        []byte
	startOffset int64
	endOffset   int64
	nextID      uint64
	subscribers map[uint64]chan VideoLogChunk
}

func newVideoLogBuffer(capacity int) *videoLogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &videoLogBuffer{
		capacity:    capacity,
		data:        make([]byte, 0, capacity),
		subscribers: make(map[uint64]chan VideoLogChunk),
	}
}

func (buffer *videoLogBuffer) Write(chunk []byte) (int, error) {
	if len(chunk) == 0 {
		return 0, nil
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	offset := buffer.endOffset
	buffer.endOffset += int64(len(chunk))
	if len(chunk) >= buffer.capacity {
		buffer.data = append(buffer.data[:0], chunk[len(chunk)-buffer.capacity:]...)
	} else {
		overflow := len(buffer.data) + len(chunk) - buffer.capacity
		if overflow > 0 {
			copy(buffer.data, buffer.data[overflow:])
			buffer.data = buffer.data[:len(buffer.data)-overflow]
		}
		buffer.data = append(buffer.data, chunk...)
	}
	buffer.startOffset = buffer.endOffset - int64(len(buffer.data))
	for id, subscriber := range buffer.subscribers {
		copyOfChunk := VideoLogChunk{Offset: offset, Data: append([]byte(nil), chunk...)}
		select {
		case subscriber <- copyOfChunk:
		default:
			delete(buffer.subscribers, id)
			close(subscriber)
		}
	}
	return len(chunk), nil
}

func (buffer *videoLogBuffer) snapshot() VideoLogSnapshot {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.snapshotLocked()
}

func (buffer *videoLogBuffer) subscribeWithSnapshot() (VideoLogSnapshot, <-chan VideoLogChunk, func()) {
	buffer.mu.Lock()
	snapshot := buffer.snapshotLocked()
	id := buffer.nextID
	buffer.nextID++
	chunks := make(chan VideoLogChunk, videoLogSubscriberCapacity)
	buffer.subscribers[id] = chunks
	buffer.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			buffer.mu.Lock()
			if subscriber, exists := buffer.subscribers[id]; exists {
				delete(buffer.subscribers, id)
				close(subscriber)
			}
			buffer.mu.Unlock()
		})
	}
	return snapshot, chunks, cancel
}

func (buffer *videoLogBuffer) snapshotLocked() VideoLogSnapshot {
	return VideoLogSnapshot{
		CapacityBytes: buffer.capacity,
		StartOffset:   buffer.startOffset,
		EndOffset:     buffer.endOffset,
		Data:          append([]byte(nil), buffer.data...),
	}
}
