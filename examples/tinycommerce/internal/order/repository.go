package order

import (
	"context"
	"sync"
)

// Repository isolates the application service from persistence details.
type Repository interface {
	Save(ctx context.Context, order Order) error
	Find(ctx context.Context, id string) (Order, bool)
}

type MemoryRepository struct {
	mu     sync.RWMutex
	orders map[string]Order
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{orders: make(map[string]Order)}
}

func (r *MemoryRepository) Save(_ context.Context, order Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders[order.ID] = order
	return nil
}

func (r *MemoryRepository) Find(_ context.Context, id string) (Order, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, found := r.orders[id]
	return value, found
}
