package models

type AuthRequest struct {
	AppId   int    `json:"appId"`
	AppHash string `json:"appHash"`
}
