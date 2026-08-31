package worker

import "sync"

const logSubscriberCapacity = 64

type LogSnapshot struct {
	StartOffset int64
	EndOffset   int64
	Data        []byte
}

type LogChunk struct {
	Offset int64
	Data   []byte
}

type LogBuffer struct {
	mu          sync.RWMutex
	capacity    int
	bytes       []byte
	startOffset int64
	endOffset   int64
	nextID      int
	subscribers map[int]chan LogChunk
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &LogBuffer{capacity: capacity, subscribers: make(map[int]chan LogChunk)}
}

func (buffer *LogBuffer) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	offset := buffer.endOffset
	buffer.endOffset += int64(len(data))
	buffer.bytes = append(buffer.bytes, data...)
	if extra := len(buffer.bytes) - buffer.capacity; extra > 0 {
		buffer.bytes = append([]byte(nil), buffer.bytes[extra:]...)
	}
	buffer.startOffset = buffer.endOffset - int64(len(buffer.bytes))
	for id, subscriber := range buffer.subscribers {
		chunk := LogChunk{Offset: offset, Data: append([]byte(nil), data...)}
		select {
		case subscriber <- chunk:
		default:
			close(subscriber)
			delete(buffer.subscribers, id)
		}
	}
	return len(data), nil
}

func (buffer *LogBuffer) Snapshot() LogSnapshot {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	return LogSnapshot{
		StartOffset: buffer.startOffset,
		EndOffset:   buffer.endOffset,
		Data:        append([]byte(nil), buffer.bytes...),
	}
}

func (buffer *LogBuffer) Subscribe() (<-chan LogChunk, func()) {
	buffer.mu.Lock()
	id := buffer.nextID
	buffer.nextID++
	chunks := make(chan LogChunk, logSubscriberCapacity)
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
	return chunks, cancel
}
