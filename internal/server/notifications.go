package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thelicato/ghal/internal/github"
	"github.com/thelicato/ghal/internal/storage"
	"github.com/thelicato/ghal/internal/telegram"
)

type notificationJob struct {
	result     github.Result
	scope      string
	revision   uint64
	generation uint64
	test       bool
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func notificationScope(connections []github.Connection) string {
	ids := make([]string, 0, len(connections))
	for _, conn := range connections {
		ids = append(ids, conn.ID)
	}
	slices.Sort(ids)
	return digest(ids)
}

func notificationSnapshot(scope string, alerts []github.Alert) *storage.NotificationSnapshot {
	snapshot := &storage.NotificationSnapshot{Scope: scope, Alerts: make(map[string]string, len(alerts))}
	for _, alert := range alerts {
		key := strings.ToLower(alert.Repository.FullName) + "#" + strconv.Itoa(alert.Number)
		// Ignore fetch order, contributing PATs, links, and timestamps without visible changes.
		alert.ConnectionIDs, alert.HTMLURL, alert.CreatedAt, alert.UpdatedAt = nil, "", "", ""
		alert.Repository.FullName = strings.ToLower(alert.Repository.FullName)
		alert.Repository.Owner.Login = strings.ToLower(alert.Repository.Owner.Login)
		snapshot.Alerts[key] = digest(alert)
	}
	return snapshot
}

func notificationMessage(before, after *storage.NotificationSnapshot, alerts []github.Alert) string {
	var added, updated, removed []string
	for key, hash := range after.Alerts {
		prior, found := before.Alerts[key]
		if !found {
			added = append(added, key)
		} else if prior != hash {
			updated = append(updated, key)
		}
	}
	for key := range before.Alerts {
		if _, found := after.Alerts[key]; !found {
			removed = append(removed, key)
		}
	}
	if len(added)+len(updated)+len(removed) == 0 {
		return ""
	}
	details := make(map[string]string, len(alerts))
	for _, alert := range alerts {
		key := strings.ToLower(alert.Repository.FullName) + "#" + strconv.Itoa(alert.Number)
		details[key] = fmt.Sprintf("%s [%s, %s] %s", key, alert.SecurityAdvisory.Severity, alert.State, alert.Dependency.Package.Name)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "ghal: Dependabot alerts changed\nNew: %d | Updated: %d | Removed: %d\n", len(added), len(updated), len(removed))
	shown := 0
	for _, group := range []struct {
		label string
		keys  []string
	}{{"New", added}, {"Updated", updated}, {"Removed", removed}} {
		slices.Sort(group.keys)
		for _, key := range group.keys {
			if shown == 12 {
				break
			}
			line := key
			if group.label != "Removed" {
				line = details[key]
			}
			chars := []rune(strings.Join(strings.Fields(line), " "))
			if len(chars) > 140 {
				chars = append(chars[:137], '.', '.', '.')
			}
			fmt.Fprintf(&text, "\n%s: %s", group.label, string(chars))
			shown++
		}
	}
	if remaining := len(added) + len(updated) + len(removed) - shown; remaining > 0 {
		fmt.Fprintf(&text, "\n\n%d more changes.", remaining)
	}
	text.WriteString("\n\nOpen ghal to review the full results.")
	return text.String()
}

func (s *Server) wakeMessages() {
	select {
	case s.messageWake <- struct{}{}:
	default:
	}
}

// A single worker serializes history updates. While busy, only the latest resync is queued.
func (s *Server) messageLoop() {
	defer s.jobs.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.messageWake:
		}
		for {
			s.state.mu.Lock()
			job := s.state.testPending
			if job != nil {
				s.state.testPending = nil
			} else {
				job, s.state.messagePending = s.state.messagePending, nil
			}
			if job == nil || s.closed {
				s.state.mu.Unlock()
				break
			}
			s.state.telegramBusy = true
			s.state.mu.Unlock()
			s.deliverNotification(job)
			s.state.mu.Lock()
			s.state.telegramBusy = false
			s.state.mu.Unlock()
		}
	}
}

// The caller holds state.mu.
func (s *Server) validNotification(job *notificationJob) bool {
	return !s.closed && job.generation == s.state.telegramGeneration && (job.test || job.revision == s.state.revision)
}

func (s *Server) deliverNotification(job *notificationJob) {
	var snapshot *storage.NotificationSnapshot
	if !job.test {
		snapshot = notificationSnapshot(job.scope, job.result.Alerts)
	}
	s.state.mu.Lock()
	config := s.state.telegram
	if !s.validNotification(job) || config.Token == "" || (!job.test && !config.Enabled) {
		s.state.mu.Unlock()
		return
	}
	message := "ghal: Telegram test message. This chat can receive notifications from ghal."
	if !job.test {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		before, err := s.store.TelegramSnapshot(ctx)
		cancel()
		if err != nil {
			s.state.telegramError = "Could not load Telegram change history. Save Telegram settings to reset it."
			s.state.mu.Unlock()
			return
		}
		message = ""
		if before != nil && before.Scope == snapshot.Scope {
			message = notificationMessage(before, snapshot, job.result.Alerts)
		}
		if message == "" {
			s.saveNotificationHistory(snapshot)
			s.state.mu.Unlock()
			return
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	s.state.telegramCancel = cancel
	s.state.mu.Unlock()
	err := s.telegram.Send(ctx, config.Token, config.ChatID, message)
	cancel()
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.telegramCancel = nil
	if !s.validNotification(job) {
		return
	}
	if err != nil {
		var apiError *telegram.APIError
		s.state.telegramError = "Could not send the Telegram message. Check the configuration and try a test message. Changes will be retried after a later successful resync."
		if errors.As(err, &apiError) {
			s.state.telegramError = apiError.Error()
		}
		return
	}
	s.state.telegramError = ""
	s.state.telegramLastSent = time.Now().UTC()
	if !job.test {
		s.saveNotificationHistory(snapshot)
	}
}

// The caller holds state.mu, so configuration changes cannot overwrite this history.
func (s *Server) saveNotificationHistory(snapshot *storage.NotificationSnapshot) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := s.store.SetTelegramSnapshot(ctx, snapshot); err != nil {
		s.state.telegramError = "Could not save Telegram change history to SQLite."
	} else {
		s.state.telegramError = ""
	}
}
