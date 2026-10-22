package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAcceptedAlertsUpgradePersistAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ghal.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a deployment database created before local acceptance existed.
	if _, err := store.db.Exec(`DROP TABLE accepted_alerts`); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(ctx, savedConnection("one")); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at, err := store.AcceptAlert(ctx, AlertID{"ACME/Project", 1})
	if err != nil || at.IsZero() {
		t.Fatal("acceptance was not saved after upgrading the database", err)
	}
	if _, err := store.AcceptAlert(ctx, AlertKey("acme/project", 2)); err != nil {
		t.Fatal(err)
	}
	again, err := store.AcceptAlert(ctx, AlertKey("acme/project", 1))
	if err != nil || !again.Equal(at) {
		t.Fatal("repeated accept changed the decision time", err)
	}
	_ = store.Close()
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.ListAcceptedAlerts(ctx)
	if err != nil || len(accepted) != 2 || !accepted[AlertKey("acme/project", 1)].Equal(at) {
		t.Fatal("acceptance did not survive restart", err)
	}
	connections, err := store.List(ctx)
	if err != nil || len(connections) != 1 || connections[0].Token != savedConnection("one").Token {
		t.Fatal("database upgrade lost saved connections", err)
	}
	if err := store.ReopenAlert(ctx, AlertID{"ACME/PROJECT", 1}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err = store.ListAcceptedAlerts(ctx)
	if err != nil || len(accepted) != 1 || accepted[AlertKey("acme/project", 2)].IsZero() {
		t.Fatal("reopen did not persist or changed a different alert", err)
	}
}
