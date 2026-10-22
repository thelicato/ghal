// Package storage persists shared GitHub connections and alert decisions in SQLite.
package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/thelicato/ghal/internal/github"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

// These values identify the database format independently of application naming.
const keyCheckID = "connection-store/key-check/v1"
const keyCheckValue = "ok"

// Open creates the database and a 32-byte encryption key on first use.
// An existing database requires its original key; keys are never overwritten.
func Open(path, keyPath string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid database path")
	}
	if keyPath == "" {
		keyPath = path + ".key"
	}
	keyPath, err = filepath.Abs(keyPath)
	if err != nil || keyPath == path {
		return nil, errors.New("encryption key must use a separate file")
	}
	_, statErr := os.Stat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, errors.New("could not access database file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("could not create database directory")
	}
	key, err := loadKey(keyPath, errors.Is(statErr, os.ErrNotExist))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("invalid database encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("could not initialize token encryption")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("could not open database file")
	}
	chmodErr := file.Chmod(0600)
	closeErr := file.Close()
	if chmodErr != nil || closeErr != nil {
		return nil, errors.New("could not secure database file")
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{
		"_busy_timeout": {"5000"}, "_journal_mode": {"WAL"},
		"_synchronous": {"FULL"}, "_foreign_keys": {"on"}, "_secure_delete": {"on"},
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, errors.New("could not open SQLite database")
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, aead: aead}
	if err := s.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func loadKey(path string, allowCreate bool) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("encryption key file must contain exactly 32 raw bytes")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("could not read database encryption key")
	}
	if !allowCreate {
		return nil, errors.New("database encryption key is missing; restore the original key file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("could not create encryption key directory")
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("could not generate database encryption key")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return loadKey(path, false)
	}
	if err != nil {
		return nil, errors.New("could not create database encryption key")
	}
	_, writeErr := file.Write(key)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return nil, errors.New("could not save database encryption key")
	}
	return key, nil
}

func (s *Store) initialize() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS metadata (name TEXT PRIMARY KEY, value BLOB NOT NULL);
		CREATE TABLE IF NOT EXISTS connections (
			id TEXT PRIMARY KEY,
			label TEXT NOT NULL,
			login TEXT NOT NULL,
			accounts TEXT NOT NULL,
			organizations TEXT NOT NULL,
			token BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS accepted_alerts (
			repository TEXT NOT NULL,
			alert_number INTEGER NOT NULL CHECK (alert_number > 0),
			accepted_at TEXT NOT NULL,
			PRIMARY KEY (repository, alert_number)
		);
	`)
	if err != nil {
		return errors.New("could not initialize SQLite database")
	}
	check, err := s.encrypt(keyCheckID, keyCheckValue)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO metadata (name, value) VALUES ('key-check', ?)`, check); err != nil {
		return errors.New("could not verify database encryption key")
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT value FROM metadata WHERE name = 'key-check'`).Scan(&stored); err != nil {
		return errors.New("could not read database encryption metadata")
	}
	plain, err := s.decrypt(keyCheckID, stored)
	if err != nil || plain != keyCheckValue {
		// Databases created before the rename used the old application name here.
		plain, err = s.decrypt("dbaui/key-check", stored)
		if err != nil || plain != "dbaui" {
			return errors.New("database encryption key does not match; restore the original key file")
		}
		if _, err := s.db.Exec(`UPDATE metadata SET value = ? WHERE name = 'key-check'`, check); err != nil {
			return errors.New("could not upgrade database encryption metadata")
		}
	}
	return nil
}

func (s *Store) encrypt(id, token string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("could not encrypt GitHub token")
	}
	return s.aead.Seal(nonce, nonce, []byte(token), []byte(id)), nil
}

func (s *Store) decrypt(id string, encrypted []byte) (string, error) {
	if len(encrypted) < s.aead.NonceSize() {
		return "", errors.New("stored GitHub token is damaged")
	}
	nonce := encrypted[:s.aead.NonceSize()]
	plain, err := s.aead.Open(nil, nonce, encrypted[s.aead.NonceSize():], []byte(id))
	if err != nil {
		return "", errors.New("could not decrypt stored GitHub token")
	}
	return string(plain), nil
}

func (s *Store) List(ctx context.Context) ([]github.Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, login, accounts, organizations, token FROM connections ORDER BY rowid`)
	if err != nil {
		return nil, errors.New("could not load saved connections")
	}
	defer rows.Close()
	connections := make([]github.Connection, 0)
	for rows.Next() {
		var conn github.Connection
		var accounts, orgs string
		var encrypted []byte
		if err := rows.Scan(&conn.ID, &conn.Label, &conn.Login, &accounts, &orgs, &encrypted); err != nil {
			return nil, errors.New("could not read saved connection")
		}
		if json.Unmarshal([]byte(accounts), &conn.Accounts) != nil || json.Unmarshal([]byte(orgs), &conn.Organizations) != nil {
			return nil, errors.New("saved connection has invalid account data")
		}
		conn.Token, err = s.decrypt(conn.ID, encrypted)
		if err != nil {
			return nil, err
		}
		connections = append(connections, conn)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not finish loading saved connections")
	}
	return connections, nil
}

func (s *Store) Add(ctx context.Context, conn github.Connection) error {
	encrypted, err := s.encrypt(conn.ID, conn.Token)
	if err != nil {
		return err
	}
	accounts, _ := json.Marshal(conn.Accounts)
	orgs, _ := json.Marshal(conn.Organizations)
	_, err = s.db.ExecContext(ctx, `INSERT INTO connections (id, label, login, accounts, organizations, token) VALUES (?, ?, ?, ?, ?, ?)`, conn.ID, conn.Label, conn.Login, string(accounts), string(orgs), encrypted)
	if err != nil {
		return errors.New("could not save connection")
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id)
	if err != nil {
		return errors.New("could not delete saved connection")
	}
	return nil
}

// UpdateVerifiedConnection saves resolved defaults without rewriting the encrypted PAT.
func (s *Store) UpdateVerifiedConnection(ctx context.Context, conn github.Connection) error {
	accounts, _ := json.Marshal(conn.Accounts)
	_, err := s.db.ExecContext(ctx, `UPDATE connections SET label = ?, login = ?, accounts = ? WHERE id = ?`, conn.Label, conn.Login, string(accounts), conn.ID)
	if err != nil {
		return errors.New("could not save verified connection")
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) Close() error                   { return s.db.Close() }
