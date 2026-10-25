package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const DefaultSyncInterval = time.Hour
const MinSyncInterval = time.Minute

// SyncInterval also supplies the default for deployments created before settings existed.
func (s *Store) SyncInterval(ctx context.Context) (time.Duration, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE name = 'sync-interval'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSyncInterval, nil
	}
	if err != nil {
		return 0, errors.New("could not load automatic resync interval")
	}
	interval, err := time.ParseDuration(value)
	if err != nil || interval < MinSyncInterval {
		return 0, errors.New("saved automatic resync interval is invalid")
	}
	return interval, nil
}

func (s *Store) SetSyncInterval(ctx context.Context, interval time.Duration) error {
	if interval < MinSyncInterval {
		return errors.New("automatic resync interval must be at least one minute")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (name, value) VALUES ('sync-interval', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, interval.String())
	if err != nil {
		return errors.New("could not save automatic resync interval")
	}
	return nil
}
