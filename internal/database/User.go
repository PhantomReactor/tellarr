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
	result, err := u.db.NamedExec(`insert into users (user, passowrd_hash, created_at, updated_at) values
		(:username, :password_hash, :created_at, :updated_at)`, user)
	if err != nil {
		return 0, nil
	}
	return result.LastInsertId()
}

func (u *UserRepository) UpdateUser(user models.User) error {
	_, err := u.db.NamedExec(`update users set username = :username, password_hash = :password_hash, updated_at = :updated_at`, user)
	if err != nil {
		return err
	}
	return nil
}

func (u *UserRepository) GetUser(username string, id int64) (*models.User, error) {
	if id == 0 && username == "" {
		return nil, fmt.Errorf("id or username is required")
	}

	var user models.User
	if id != 0 {
		err := u.db.Get(&user, "select * from users where id = ?", id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
	}
	err := u.db.Get(&user, "select * from users where username = ?", username)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &user, nil
}
