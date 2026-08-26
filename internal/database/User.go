package database

import (
	"database/sql"
	"errors"
	"fmt"
	"tellarr/internal/database/models"

	"github.com/jmoiron/sqlx"
)

type UserRepository struct {
	db *sqlx.DB
}

func NewUserRespository(db *sqlx.DB) UserRepository {
	return UserRepository{db: db}
}

func (u *UserRepository) CreateUser(user models.User) (int64, error) {
	result, err := u.db.NamedExec(`insert into users (username, password_hash, created_at, updated_at) values
		(:username, :password_hash, :created_at, :updated_at)`, user)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (u *UserRepository) UpdateUser(user models.User) error {
	_, err := u.db.NamedExec(`update users set username = :username, password_hash = :password_hash, updated_at = :updated_at where id = :id`, user)
	if err != nil {
		return err
	}
	return nil
}

// HasAnyUser reports whether at least one account exists. Tellarr is
// single-user: registration is only offered until the first account is made.
func (u *UserRepository) HasAnyUser() (bool, error) {
	var count int
	if err := u.db.Get(&count, "select count(*) from users"); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (u *UserRepository) GetUser(username string, id int64) (*models.User, error) {
	if id == 0 && username == "" {
		return nil, fmt.Errorf("id or username is required")
	}

	var user models.User
	if id != 0 {
		err := u.db.Get(&user, "select * from users where id = ?", id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		return &user, nil
	}
	err := u.db.Get(&user, "select * from users where username = ?", username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}
