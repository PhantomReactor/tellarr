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
	Name      string `json:"name,omitempty"`
	Link      string `json:"link,omitempty"`
	Size      int64  `json:"size,omitempty"`
	MessageId int64  `json:"messageId,omitempty"`
	SessionId int64  `json:"sessionId,omitempty"`
	DialogId  int64  `json:"dialogId,omitempty"`
	IsTorrent bool   `json:"isTorrent,omitempty"`
}

type DialogInfo struct {
	Name       string `json:"name,omitempty"`
	Id         int64  `json:"id,omitempty"`
	AccessHash int64  `json:"accessHash,omitempty"`
}

type DownloadInfo struct {
	Name    string  `json:"name,omitempty"`
	Id      string  `json:"id,omitempty"`
	Percent float64 `json:"percent,omitempty"`
	State   string  `json:"state,omitempty"`
	Size    int64   `json:"size,omitempty"`
}

type AuthResponse struct {
	Id           int64  `json:"id,omitempty"`
	Username     string `json:"username,omitempty"`
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

func NewResponse(w http.ResponseWriter, response any, status int) {
	w.WriteHeader(status)
	if response != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}
