package order

import (
	"context"
	"testing"
)

func TestServiceSubmitPersistsValidOrder(t *testing.T) {
	repository := NewMemoryRepository()
	service := NewService(repository)
	input := Order{ID: "order-1", CustomerID: "customer-1", TotalCents: 4200}

	if err := service.Submit(context.Background(), input); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if _, found := repository.Find(context.Background(), input.ID); !found {
		t.Fatal("submitted order was not persisted")
	}
}
