package models

import (
	"encoding/json"
	"net/http"
)

type AppResponse struct {
	Message   string `json:"message"`
	PhoneHash string `json:"phoneHash"`
}

func NewAppResponse(w http.ResponseWriter, message string, phoneHash string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(AppResponse{Message: message, PhoneHash: phoneHash})
}
