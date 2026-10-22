package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

// AlertID identifies one repository alert independently of its PAT connections.
type AlertID struct {
	Repository string
	Number     int
}

func AlertKey(repository string, number int) AlertID {
	return AlertID{Repository: strings.ToLower(repository), Number: number}
}

func (s *Store) ListAcceptedAlerts(ctx context.Context) (map[AlertID]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository, alert_number, accepted_at FROM accepted_alerts`)
	if err != nil {
		return nil, errors.New("could not load accepted alerts")
	}
	defer rows.Close()
	accepted := make(map[AlertID]time.Time)
	for rows.Next() {
		var id AlertID
		var timestamp string
		if err := rows.Scan(&id.Repository, &id.Number, &timestamp); err != nil {
			return nil, errors.New("could not read accepted alert")
		}
		at, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, errors.New("accepted alert has an invalid timestamp")
		}
		accepted[AlertKey(id.Repository, id.Number)] = at
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not finish loading accepted alerts")
	}
	return accepted, nil
}

func (s *Store) AcceptAlert(ctx context.Context, id AlertID) (time.Time, error) {
	accepted, err := s.AcceptAlerts(ctx, []AlertID{id})
	if err != nil {
		return time.Time{}, err
	}
	return accepted[AlertKey(id.Repository, id.Number)], nil
}

// AcceptAlerts saves the whole selection atomically and retains existing decision times.
func (s *Store) AcceptAlerts(ctx context.Context, ids []AlertID) (map[AlertID]time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("could not save accepted alerts")
	}
	defer tx.Rollback()
	accepted := make(map[AlertID]time.Time, len(ids))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		id = AlertKey(id.Repository, id.Number)
		var timestamp string
		err := tx.QueryRowContext(ctx, `
			INSERT INTO accepted_alerts (repository, alert_number, accepted_at) VALUES (?, ?, ?)
			ON CONFLICT (repository, alert_number) DO UPDATE SET accepted_at = accepted_alerts.accepted_at
			RETURNING accepted_at`, id.Repository, id.Number, now).Scan(&timestamp)
		if err != nil {
			return nil, errors.New("could not save accepted alerts")
		}
		at, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, errors.New("accepted alert has an invalid timestamp")
		}
		accepted[id] = at
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("could not save accepted alerts")
	}
	return accepted, nil
}

func (s *Store) ReopenAlert(ctx context.Context, id AlertID) error {
	id = AlertKey(id.Repository, id.Number)
	_, err := s.db.ExecContext(ctx, `DELETE FROM accepted_alerts WHERE repository = ? AND alert_number = ?`, id.Repository, id.Number)
	if err != nil {
		return errors.New("could not reopen accepted alert")
	}
	return nil
}
