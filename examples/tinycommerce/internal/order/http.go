package order

import (
	"encoding/json"
	"net/http"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Create(response http.ResponseWriter, request *http.Request) {
	var input Order
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		http.Error(response, "invalid payload", http.StatusBadRequest)
		return
	}
	if err := h.service.Submit(request.Context(), input); err != nil {
		http.Error(response, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	response.WriteHeader(http.StatusCreated)
}
