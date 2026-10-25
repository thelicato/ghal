package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thelicato/ghal/internal/telegram"
)

func TestTelegramSettingsAreEncryptedAndHistorySurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ghal.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	config, err := store.TelegramConfig(ctx)
	if err != nil || config.Enabled || config.Token != "" {
		t.Fatal("new deployments have Telegram enabled")
	}
	config = telegram.Config{Enabled: true, Token: "123:telegram-secret-never-on-disk", ChatID: "-10012345"}
	want := &NotificationSnapshot{Scope: "scope", Alerts: map[string]string{"acme/project#1": "fingerprint"}}
	if err := store.SetTelegramConfig(ctx, config, want); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		data, err := os.ReadFile(file)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(config.Token)) {
			t.Fatal("Telegram token found in SQLite files")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.TelegramConfig(ctx)
	if err != nil || restored != config {
		t.Fatal("encrypted Telegram configuration did not survive restart")
	}
	history, err := store.TelegramSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(history, want) {
		t.Fatal("Telegram change history did not survive restart")
	}
	if err := store.SetTelegramConfig(ctx, telegram.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	restored, err = store.TelegramConfig(ctx)
	if err != nil || restored.Token != "" || restored.ChatID != "" || restored.Enabled {
		t.Fatal("disconnect did not clear Telegram credentials")
	}
	history, err = store.TelegramSnapshot(ctx)
	if err != nil || history != nil {
		t.Fatal("disconnect did not reset notification history")
	}
}

func TestFailedTelegramConfigurationWriteDoesNotPartiallyResetHistory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "ghal.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config := telegram.Config{Enabled: true, Token: "123:original-secret", ChatID: "42"}
	history := &NotificationSnapshot{Scope: "old", Alerts: map[string]string{"acme/project#1": "old"}}
	if err := store.SetTelegramConfig(ctx, config, history); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_telegram_history BEFORE INSERT ON metadata WHEN NEW.name = 'telegram-snapshot/v1' BEGIN SELECT RAISE(ABORT, 'history unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	updated := telegram.Config{Enabled: true, Token: "456:replacement-secret", ChatID: "43"}
	if err := store.SetTelegramConfig(ctx, updated, &NotificationSnapshot{Scope: "new", Alerts: map[string]string{}}); err == nil {
		t.Fatal("partially failed settings update was accepted")
	}
	got, err := store.TelegramConfig(ctx)
	if err != nil || got != config {
		t.Fatal("failed transaction changed encrypted Telegram configuration")
	}
	gotHistory, err := store.TelegramSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(gotHistory, history) {
		t.Fatal("failed transaction changed notification history")
	}
	if _, err := store.db.Exec(`UPDATE metadata SET value = ? WHERE name = ?`, []byte("damaged"), telegramConfigKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TelegramConfig(ctx); err == nil {
		t.Fatal("damaged encrypted Telegram settings were accepted")
	}
}
