package database

import (
	"database/sql"
	"errors"
	"tellarr/internal/database/models"

	"github.com/jmoiron/sqlx"
)

type DialogsRepository struct {
	db *sqlx.DB
}

func NewDialogsRepository(db *sqlx.DB) DialogsRepository {
	return DialogsRepository{db: db}
}

func (d *DialogsRepository) CreateDialog(dialog models.Dialog) (int64, error) {
	result, err := d.db.NamedExec(`INSERT INTO DIALOGS (session_id, phone_number, name, type, dialog_id, access_hash, indexer, active, created_at, updated_at) 
		VALUES (:session_id, :phone_number, :name, :type, :dialog_id, :access_hash, :indexer, :active, :created_at, :updated_at)`, dialog)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (d *DialogsRepository) UpdateDialog(dialog models.Dialog) error {
	_, err := d.db.NamedExec(
		`update dialogs set session_id = :session_id, phone_number = :phone_number, name = :name, type = :type, dialog_id = :dialog_id, access_hash = :access_hash, indexer = :indexer,
		active = :active, created_at = :created_at, updated_at = :updated_at where id = :id`, dialog)
	if err != nil {
		return err
	}
	return nil
}

func (d *DialogsRepository) GetDialogByName(name string) (*models.Dialog, error) {
	var dialog models.Dialog
	err := d.db.Get(&dialog, "select * from dialogs where name = ?", name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dialog, nil
}

func (d *DialogsRepository) GetDialogChannelId(name string) (*models.Dialog, error) {
	var dialog models.Dialog
	err := d.db.Get(&dialog, "select * from dialogs where name = ?", name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dialog, nil
}

func (d *DialogsRepository) GetDialogsByDialogId(dialogId int64) (*models.Dialog, error) {
	var dialog models.Dialog
	err := d.db.Get(&dialog, "select * from dialogs where dialog_id = ?", dialogId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dialog, nil
}
