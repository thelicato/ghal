package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thelicato/ghal/internal/telegram"
)

const telegramConfigKey = "telegram-config/v1"
const telegramSnapshotKey = "telegram-snapshot/v1"

type telegramRecord struct {
	Enabled bool
	ChatID  string
	Token   string
}

// NotificationSnapshot stores fingerprints rather than alert contents or credentials.
type NotificationSnapshot struct {
	Scope  string            `json:"scope"`
	Alerts map[string]string `json:"alerts"`
}

func (s *Store) TelegramConfig(ctx context.Context) (telegram.Config, error) {
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE name = ?`, telegramConfigKey).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return telegram.Config{}, nil
	}
	if err != nil {
		return telegram.Config{}, errors.New("could not load Telegram settings")
	}
	plain, err := s.decrypt(telegramConfigKey, encrypted)
	if err != nil {
		return telegram.Config{}, errors.New("could not decrypt Telegram settings")
	}
	var record telegramRecord
	if json.Unmarshal([]byte(plain), &record) != nil {
		return telegram.Config{}, errors.New("stored Telegram settings are invalid")
	}
	config := telegram.Config{Enabled: record.Enabled, ChatID: record.ChatID, Token: record.Token}
	if config.Validate() != nil {
		return telegram.Config{}, errors.New("stored Telegram settings are invalid")
	}
	return config, nil
}

// SetTelegramConfig resets change history atomically when the destination changes.
// A nil snapshot clears the history until the next complete resync.
func (s *Store) SetTelegramConfig(ctx context.Context, config telegram.Config, snapshot *NotificationSnapshot) error {
	if err := config.Validate(); err != nil {
		return err
	}
	plain, _ := json.Marshal(telegramRecord{config.Enabled, config.ChatID, config.Token})
	encrypted, err := s.encrypt(telegramConfigKey, string(plain))
	if err != nil {
		return errors.New("could not encrypt Telegram settings")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("could not save Telegram settings")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata (name, value) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value`, telegramConfigKey, encrypted); err != nil {
		return errors.New("could not save Telegram settings")
	}
	if snapshot == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM metadata WHERE name = ?`, telegramSnapshotKey)
	} else {
		value, _ := json.Marshal(snapshot)
		_, err = tx.ExecContext(ctx, `INSERT INTO metadata (name, value) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value`, telegramSnapshotKey, value)
	}
	if err != nil || tx.Commit() != nil {
		return errors.New("could not save Telegram settings")
	}
	return nil
}

func (s *Store) TelegramSnapshot(ctx context.Context) (*NotificationSnapshot, error) {
	var value []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE name = ?`, telegramSnapshotKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not load Telegram change history")
	}
	var snapshot NotificationSnapshot
	if json.Unmarshal(value, &snapshot) != nil || snapshot.Scope == "" || snapshot.Alerts == nil {
		return nil, errors.New("stored Telegram change history is invalid")
	}
	return &snapshot, nil
}

func (s *Store) SetTelegramSnapshot(ctx context.Context, snapshot *NotificationSnapshot) error {
	value, _ := json.Marshal(snapshot)
	_, err := s.db.ExecContext(ctx, `INSERT INTO metadata (name, value) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value`, telegramSnapshotKey, value)
	if err != nil {
		return errors.New("could not save Telegram change history")
	}
	return nil
}
