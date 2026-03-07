package database

import (
	"context"
	"github.com/gotd/td/session"
)

type DBSessionStorage struct {
	SessionRepository SessionRepository
	SessionID         int64
}

func (d *DBSessionStorage) LoadSession(_ context.Context) ([]byte, error) {
	if d.SessionID == 0 {
		return nil, session.ErrNotFound
	}
	s, err := d.SessionRepository.GetSession(d.SessionID, "")
	if err != nil {
		return nil, err
	}
	if s == nil || s.Token == "" {
		return nil, session.ErrNotFound
	}
	return []byte(s.Token), nil
}

func (d *DBSessionStorage) StoreSession(_ context.Context, token []byte) error {
	return d.SessionRepository.UpdateToken(d.SessionID, string(token))
}
