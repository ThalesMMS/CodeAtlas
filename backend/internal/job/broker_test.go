package job

import (
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func TestBrokerSerializesSinkDeliveryWithSequenceAssignment(t *testing.T) {
	broker := newBroker(1)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDelivered := make(chan struct{})
	broker.setSink(func(event domain.JobEvent) {
		switch event.ID {
		case "evt-1":
			close(firstStarted)
			<-releaseFirst
		case "evt-2":
			close(secondDelivered)
		}
	})

	firstDone := make(chan struct{})
	go func() {
		broker.publish(domain.JobEvent{Type: "first"})
		close(firstDone)
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first event did not reach sink")
	}

	secondDone := make(chan struct{})
	go func() {
		broker.publish(domain.JobEvent{Type: "second"})
		close(secondDone)
	}()
	select {
	case <-secondDelivered:
		t.Fatal("second event reached sink before first delivery completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first publish did not finish")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second publish did not finish")
	}
	select {
	case <-secondDelivered:
	default:
		t.Fatal("second event was not delivered after first completed")
	}
}
