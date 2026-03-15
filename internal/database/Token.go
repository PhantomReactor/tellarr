package database

import (
	"database/sql"
	"errors"
	"tellarr/internal/database/models"
	"tellarr/internal/pkg/enums"

	"github.com/jmoiron/sqlx"
)

type TokenRepository struct {
	db *sqlx.DB
}

func NewTokenRepository(db *sqlx.DB) TokenRepository {
	return TokenRepository{db: db}
}

func (t *TokenRepository) CreateToken(token *models.Token) (int64, error) {
	result, err := t.db.NamedExec(`insert into tokens (user_id, token, type, expires_at, created_at, updated_at)
		values (:user_id, :token, :type, :expires_at, :created_at, :updated_at)`, token)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (t *TokenRepository) UpdateToken(token *models.Token) error {
	_, err := t.db.NamedExec(`update tokens set user_id = :user_id, token = :token, type = :type, expires_at = :expires_at,
		created_at = :created_at, updated_at = :updated_at`, token)
	if err != nil {
		return err
	}
	return nil
}

func (t *TokenRepository) GetTokenById(id int64, tokenType enums.TokenType) (*models.Token, error) {
	var refreshToken models.Token
	if err := t.db.Get(&refreshToken, "select * from tokens where id = ? and type = ?", id, tokenType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &refreshToken, nil
}

func (t *TokenRepository) GetTokensByTokenType(tokenType enums.TokenType) (*[]models.Token, error) {
	var tokens []models.Token
	if err := t.db.Select(&tokens, "select * from tokens where type = ?", tokenType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &tokens, nil
}

func (t *TokenRepository) GetToken(userId int64, tokenType enums.TokenType) (*models.Token, error) {
	var refreshToken models.Token
	if err := t.db.Get(&refreshToken, "select * from tokens where user_id = ? and type = ?", userId, tokenType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &refreshToken, nil
}

func (t *TokenRepository) Delete(id int64) error {
	_, err := t.db.Exec("delete from tokens where id = ?", id)
	if err != nil {
		return err
	}
	return nil
}

func (t *TokenRepository) DeleteByUserId(userId int64, tokentype enums.TokenType) error {
	_, err := t.db.Exec("delete from tokens where user_id = ? and type = ?", userId, tokentype)
	if err != nil {
		return err
	}
	return nil
}
