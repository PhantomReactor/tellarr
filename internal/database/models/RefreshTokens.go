package models

import (
	"tellarr/internal/pkg/enums"
	"time"
)

type Token struct {
	ID        int64           `db:"id"`
	UserId    int64           `db:"user_id"`
	Token     string          `db:"token"`
	Type      enums.TokenType `db:"type"`
	ExpiresAt time.Time       `db:"expires_at"`
	CreatedAt time.Time       `db:"created_at"`
	UpdatedAt time.Time       `db:"updated_at"`
}
