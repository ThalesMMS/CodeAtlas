package job

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type broker struct {
	mu          sync.RWMutex
	publishMu   sync.Mutex
	subscribers map[chan domain.JobEvent]struct{}
	sequence    uint64
	dropped     uint64
	buffer      int
	sink        func(domain.JobEvent)
}

func (b *broker) setSink(sink func(domain.JobEvent)) {
	b.mu.Lock()
	b.sink = sink
	b.mu.Unlock()
}

func newBroker(buffer int) *broker {
	if buffer <= 0 {
		buffer = 16
	}
	return &broker{subscribers: make(map[chan domain.JobEvent]struct{}), buffer: buffer}
}

func (b *broker) subscribe() (<-chan domain.JobEvent, func()) {
	channel := make(chan domain.JobEvent, b.buffer)
	b.mu.Lock()
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		if _, ok := b.subscribers[channel]; ok {
			delete(b.subscribers, channel)
			close(channel)
		}
		b.mu.Unlock()
	}
}

func (b *broker) publish(event domain.JobEvent) {
	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	event.ID = fmt.Sprintf("evt-%d", atomic.AddUint64(&b.sequence, 1))
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	b.mu.RLock()
	sink := b.sink
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
			atomic.AddUint64(&b.dropped, 1)
		}
	}
	b.mu.RUnlock()
	if sink != nil {
		sink(event)
	}
}

func (b *broker) droppedCount() uint64 {
	return atomic.LoadUint64(&b.dropped)
}
