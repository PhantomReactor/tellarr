package models

type AuthRequest struct {
	AppId     int    `json:"appId"`
	AppHash   string `json:"appHash"`
	Code      string `json:"code"`
	Phone     string `json:"phone"`
	Password  string `json:"password"`
	PhoneHash string `json:"phoneHash"`
}
