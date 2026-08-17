package indexer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

type Broker struct {
	mu          sync.RWMutex
	subscribers map[chan domain.IndexEvent]struct{}
	sequence    uint64
	dropped     uint64
	sink        func(domain.IndexEvent)
}

// SetSink installs the single downstream stream sink. Regular subscribers remain
// available for local observers and tests.
func (b *Broker) SetSink(sink func(domain.IndexEvent)) {
	b.mu.Lock()
	b.sink = sink
	b.mu.Unlock()
}

func NewBroker() *Broker {
	return &Broker{subscribers: make(map[chan domain.IndexEvent]struct{})}
}

func (b *Broker) Subscribe() (<-chan domain.IndexEvent, func()) {
	channel := make(chan domain.IndexEvent, 16)
	b.mu.Lock()
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		if _, exists := b.subscribers[channel]; exists {
			delete(b.subscribers, channel)
			close(channel)
		}
		b.mu.Unlock()
	}
}

// Publish stamps the event with a unique, monotonically increasing ID and a UTC
// timestamp, then delivers it without blocking. When a subscriber's buffer is
// full the event is dropped and a counter is incremented so the loss is
// observable (the ID gap is also visible to the client) rather than silent.
func (b *Broker) Publish(event domain.IndexEvent) {
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

// Dropped returns the number of events dropped due to full subscriber buffers.
func (b *Broker) Dropped() uint64 {
	return atomic.LoadUint64(&b.dropped)
}
