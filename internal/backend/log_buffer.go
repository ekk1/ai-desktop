package backend

import "sync"

const logSubscriberQueue = 16

type LogBuffer struct {
	mu          sync.Mutex
	capacity    int
	data        []byte
	nextID      uint64
	subscribers map[uint64]chan []byte
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &LogBuffer{
		capacity:    capacity,
		data:        make([]byte, 0, capacity),
		subscribers: make(map[uint64]chan []byte),
	}
}

func (buffer *LogBuffer) Write(chunk []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

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

	for _, subscriber := range buffer.subscribers {
		copyOfChunk := append([]byte(nil), chunk...)
		select {
		case subscriber <- copyOfChunk:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- copyOfChunk:
			default:
			}
		}
	}
	return len(chunk), nil
}

func (buffer *LogBuffer) Snapshot() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.data...)
}

func (buffer *LogBuffer) Clear() {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.data = buffer.data[:0]
}

func (buffer *LogBuffer) Subscribe() (<-chan []byte, func()) {
	buffer.mu.Lock()
	id := buffer.nextID
	buffer.nextID++
	channel := make(chan []byte, logSubscriberQueue)
	buffer.subscribers[id] = channel
	buffer.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			buffer.mu.Lock()
			delete(buffer.subscribers, id)
			close(channel)
			buffer.mu.Unlock()
		})
	}
	return channel, cancel
}
