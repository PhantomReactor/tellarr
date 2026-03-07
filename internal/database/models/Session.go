package models

import (
	"time"
)

type Session struct {
	ID            int64     `db:"id"`
	PhoneNumber   string    `db:"phone_number"`
	PhoneCodeHash string    `db:"phone_code_hash"`
	Token         string    `db:"token"`
	Active        bool      `db:"active"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}
