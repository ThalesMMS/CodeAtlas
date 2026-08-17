package order

import "time"

// Order is the aggregate accepted by the checkout flow.
type Order struct {
	ID         string
	CustomerID string
	TotalCents int64
	CreatedAt  time.Time
}
