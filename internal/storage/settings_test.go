package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncIntervalDefaultsPersistsAndRejectsInvalidChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ghal.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Add(ctx, savedConnection("existing")); err != nil {
		t.Fatal(err)
	}
	interval, err := store.SyncInterval(ctx)
	if err != nil || interval != time.Hour {
		t.Fatal("existing deployment did not default to a one-hour interval", interval, err)
	}
	want := 90*time.Minute + 15*time.Second
	if err := store.SetSyncInterval(ctx, want); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []time.Duration{-time.Hour, 0, time.Minute - time.Nanosecond} {
		if err := store.SetSyncInterval(ctx, invalid); err == nil {
			t.Fatal("invalid interval was saved", invalid)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	interval, err = store.SyncInterval(ctx)
	if err != nil || interval != want {
		t.Fatal("saved interval did not survive restart", interval, err)
	}
	connections, err := store.List(ctx)
	if err != nil || len(connections) != 1 || connections[0].Token != savedConnection("existing").Token {
		t.Fatal("saving settings changed existing encrypted connections")
	}
}

func TestInvalidStoredSyncIntervalIsReported(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, invalid := range []string{"broken", "0s", "-1h", "59s"} {
		if _, err := store.db.Exec(`INSERT OR REPLACE INTO metadata (name, value) VALUES ('sync-interval', ?)`, invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SyncInterval(context.Background()); err == nil {
			t.Fatal("invalid stored interval was accepted", invalid)
		}
	}
}
