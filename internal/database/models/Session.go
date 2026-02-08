package models

import (
	"time"
)

type Session struct {
	id          int       `db:"id"`
	PhoneNumber string    `db:"phone_number"`
	Token       string    `db:"token"`
	Active      bool      `db:"active"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}
