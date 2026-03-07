package database

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	"tellarr/internal/database/models"
)

type SessionRepository struct {
	db *sqlx.DB
}

func NewSessionRepository(db *sqlx.DB) SessionRepository {
	return SessionRepository{db: db}
}

func (s *SessionRepository) CreateSession(session models.Session) (int64, error) {
	result, err := s.db.NamedExec(`INSERT INTO SESSIONS (phone_number, phone_code_hash, token, active, created_at, updated_at) 
		VALUES (:phone_number, :phone_code_hash, :token, :active, :created_at, :updated_at)`, session)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *SessionRepository) UpdateSession(session models.Session) error {
	_, err := s.db.NamedExec(
		`update sessions set phone_number = :phone_number, phone_code_hash = :phone_code_hash, token = :token,
		active = :active, created_at = :created_at, updated_at = :updated_at where id = :id`, session)
	if err != nil {
		return err
	}
	return nil
}

func (s *SessionRepository) GetSession(id int64, phoneNumber string) (*models.Session, error) {
	var session models.Session
	if phoneNumber == "" && id == 0 {
		return nil, fmt.Errorf("id or phoneNumber rquired")
	}
	if id != 0 {
		err := s.db.Get(&session, "select * from sessions where id = ? and active = true", id)
		return &session, err
	}
	err := s.db.Get(&session, "select * from sessions where phone_number = ?", phoneNumber)
	return &session, err

}

func (s *SessionRepository) GetAllSessionIds() ([]int64, error) {
	var sessions []int64
	err := s.db.Select(&sessions, "select id from sessions where active = true")
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *SessionRepository) InactiveSession(id int64) error {
	_, err := s.db.Exec("update sessions set active = false where id = ?", id)
	return err
}

func (s *SessionRepository) DeleteSession(id int64) error {
	_, err := s.db.Exec("delete sessions where id = ?", id)
	return err
}

func (s *SessionRepository) UpdateToken(id int64, token string) error {
	_, err := s.db.Exec("update sessions set token = ? where id = ?", token, id)
	return err
}

func (s *SessionRepository) UpdatePhoneCodeHash(id int64, phoneCodeHash string) error {
	_, err := s.db.Exec("update sessions set phone_code_hash = ? where id = ?", phoneCodeHash, id)
	return err
}
