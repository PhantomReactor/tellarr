package database

import (
	"database/sql"
	"errors"
	"tellarr/internal/database/models"

	"github.com/jmoiron/sqlx"
)

type RefreshTokenRepository struct {
	db *sqlx.DB
}

func NewRefreshTokenRepository(db *sqlx.DB) RefreshTokenRepository {
	return RefreshTokenRepository{db: db}
}

func (r *RefreshTokenRepository) CreateRefreshToken(refreshToken *models.RefreshToken) (int64, error) {
	result, err := r.db.NamedExec(`insert into refresh_tokens (user_id, token, expires_at, created_at, updated_at)
		values (:user_id, :token, :expires_at, :created_at, :updated_at)`, refreshToken)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *RefreshTokenRepository) GetRefreshToken(user_id int64) (*models.RefreshToken, error) {
	var refreshToken models.RefreshToken
	if err := r.db.Get(&refreshToken, "select * from refresh_tokens where user_id = ?", user_id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &refreshToken, nil
}

func (r *RefreshTokenRepository) Delete(id int64) error {
	_, err := r.db.Exec("delete from refresh_tokens where id = ?", id)
	if err != nil {
		return err
	}
	return nil
}

func (r *RefreshTokenRepository) DeleteByUserId(userId int64) error {
	_, err := r.db.Exec("delete from refresh_tokens where user_id = ?", userId)
	if err != nil {
		return err
	}
	return nil
}
