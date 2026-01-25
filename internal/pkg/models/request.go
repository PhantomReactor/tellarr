package models

type Request struct {
	AppId      int    `json:"appId"`
	AppHash    string `json:"appHash"`
	Code       string `json:"code"`
	Phone      string `json:"phone"`
	Password   string `json:"password"`
	PhoneHash  string `json:"phoneHash"`
	ChannelID  int64  `json:"channelId"`
	AccessHash int64  `json:"accessHash"`
}
