package main

import (
	"log"
	"net/http"

	"example.com/tinycommerce/internal/order"
)

func main() {
	repository := order.NewMemoryRepository()
	service := order.NewService(repository)
	handler := order.NewHandler(service)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", handler.Create)
	log.Fatal(http.ListenAndServe("127.0.0.1:8090", mux))
}
