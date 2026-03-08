package models

type Request struct {
	SessionId    int64  `json:"sessionId"`
	AppId        int    `json:"appId"`
	AppHash      string `json:"appHash"`
	Code         string `json:"code"`
	Phone        string `json:"phone"`
	Password     string `json:"password"`
	PhoneHash    string `json:"phoneHash"`
	DialogId     int64  `json:"dialgoId"`
	AccessHash   int64  `json:"accessHash"`
	DialogName   string `json:"dialogName"`
	DownloadLink string `json:"downloadLink"`
}

type UserRequest struct {
	UserId          int64  `json:"userId"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	CurrentPassword string `json:"currentPassword"`
	Token           string `json:"token"`
}
