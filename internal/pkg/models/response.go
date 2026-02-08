package models

import (
	"encoding/json"
	"net/http"
)

type Response struct {
	Message   string `json:"message,omitempty"`
	SessionId int64  `json:"sessionId,omitempty"`
}

type MediaInfo struct {
	Name string `json:"name,omitempty"`
	Link string `json:"link,omitempty"`
}

type DialogInfo struct {
	Name       string `json:"name,omitempty"`
	Id         int64  `json:"id,omitempty"`
	AccessHash int64  `json:"accessHash,omitempty"`
}

func NewResponse(w http.ResponseWriter, response any, status int) {
	w.WriteHeader(status)
	if response != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}
