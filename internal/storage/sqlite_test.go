package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"dbaui/internal/github"
)

func savedConnection(id string) github.Connection {
	return github.Connection{ID: id, Label: "Work's token", Login: "octocat", Accounts: []string{"octocat"}, Organizations: []string{"acme", "acme-labs"}, Token: "synthetic-pat-should-never-appear-on-disk"}
}

func TestConnectionsSurviveReopenAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "dbaui.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	want := savedConnection("one")
	if err := store.Add(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.List(context.Background())
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatal("connection and associations were not restored")
	}
	if err := store.Delete(context.Background(), want.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err = store.List(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatal("deleted connection returned after restart")
	}
}

func TestTokensAreEncryptedAndFilesArePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dbaui.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"one", "two"} {
		if err := store.Add(context.Background(), savedConnection(id)); err != nil {
			t.Fatal(err)
		}
	}
	var first, second []byte
	if err := store.db.QueryRow(`SELECT token FROM connections WHERE id='one'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT token FROM connections WHERE id='two'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, []byte(savedConnection("one").Token)) {
		t.Fatal("tokens are plaintext or encryption nonces were reused")
	}
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		data, err := os.ReadFile(file)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(savedConnection("one").Token)) {
			t.Fatal("PAT found in SQLite files")
		}
	}
	for _, file := range []string{path, path + ".key"} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("private file has incorrect permissions: %s", file)
		}
	}
}

func TestWrongOrMissingKeyFailsWithoutReplacingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dbaui.db")
	store, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(context.Background(), savedConnection("one")); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	key, err := os.ReadFile(path + ".key")
	if err != nil {
		t.Fatal(err)
	}
	wrong := make([]byte, 32)
	if err := os.WriteFile(path+".key", wrong, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, ""); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("wrong key was accepted")
	}
	unchanged, _ := os.ReadFile(path + ".key")
	if !bytes.Equal(unchanged, wrong) {
		t.Fatal("existing key was replaced")
	}
	if err := os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, ""); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatal("missing key was silently regenerated")
	}
	if _, err := os.Stat(path + ".key"); !os.IsNotExist(err) {
		t.Fatal("a replacement key was created")
	}
	if err := os.WriteFile(path+".key", key, 0600); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connections, err := store.List(context.Background())
	if err != nil || len(connections) != 1 || connections[0].Token != savedConnection("one").Token {
		t.Fatal("original key did not restore the saved connection")
	}
}

func TestTamperedTokensAndSwappedRowsAreRejected(t *testing.T) {
	for _, kind := range []string{"swapped", "modified", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "dbaui.db"), "")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.Add(context.Background(), savedConnection("one")); err != nil {
				t.Fatal(err)
			}
			if err := store.Add(context.Background(), savedConnection("two")); err != nil {
				t.Fatal(err)
			}
			var data []byte
			if err := store.db.QueryRow(`SELECT token FROM connections WHERE id='one'`).Scan(&data); err != nil {
				t.Fatal(err)
			}
			target := "one"
			switch kind {
			case "swapped":
				target = "two"
			case "modified":
				data[len(data)-1] ^= 1
			case "truncated":
				data = []byte{1}
			}
			if _, err := store.db.Exec(`UPDATE connections SET token=? WHERE id=?`, data, target); err != nil {
				t.Fatal(err)
			}
			if _, err := store.List(context.Background()); err == nil {
				t.Fatal("tampered ciphertext was accepted")
			}
		})
	}
}

func TestCustomKeyFileAndInvalidKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "keys", "token.key")
	store, err := Open(filepath.Join(dir, "data", "dbaui.db"), keyPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("too-short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "other.db"), keyPath); err == nil {
		t.Fatal("invalid key length was accepted")
	}
}

func TestStorageFailuresDoNotExposeTokens(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "dbaui.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	conn := savedConnection("one")
	if err := store.Add(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(context.Background(), conn); err == nil || strings.Contains(err.Error(), conn.Token) {
		t.Fatal("duplicate connection was accepted or the error exposed its PAT")
	}
	_ = store.Close()
	if err := store.Add(context.Background(), conn); err == nil || strings.Contains(err.Error(), conn.Token) {
		t.Fatal("write to a closed store was accepted or exposed its PAT")
	}
	if err := store.Delete(context.Background(), conn.ID); err == nil {
		t.Fatal("delete from a closed store was accepted")
	}
}
