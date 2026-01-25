package models

import (
	"encoding/json"
	"net/http"
)

type Response struct {
	Message   string `json:"message,omitempty"`
	PhoneHash string `json:"phoneHash,omitempty"`
}

type Result struct {
	Name string `json:"name,omitempty"`
	Link string `json:"link,omitempty"`
}

func NewResponse(w http.ResponseWriter, response any, status int) {
	w.WriteHeader(status)
	if response != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}
