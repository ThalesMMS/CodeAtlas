package order

import (
	"context"
	"errors"
	"time"
)

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, clock: time.Now}
}

func (s *Service) Submit(ctx context.Context, order Order) error {
	if err := validateOrder(order); err != nil {
		return err
	}
	order.CreatedAt = s.clock().UTC()
	return s.repository.Save(ctx, order)
}

func validateOrder(order Order) error {
	if order.ID == "" || order.CustomerID == "" {
		return errors.New("order and customer identifiers are required")
	}
	if order.TotalCents <= 0 {
		return errors.New("order total must be positive")
	}
	return nil
}
