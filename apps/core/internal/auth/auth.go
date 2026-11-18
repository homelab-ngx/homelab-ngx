// Package auth is the dashboard's sign-in: one password, no users or roles.
// The first visitor sets the password, a right password starts a session
// held in a cookie, and Gate refuses every other request on the web socket
// without one. The CLI's socket has no gate: who can open it is root, and
// root can set or reset the password.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"homelab-ngx.com/core/internal/store"
)

// Errors the operations map onto problems.
var (
	ErrNoPassword    = errors.New("no password is set")
	ErrPasswordSet   = errors.New("a password is already set")
	ErrWrongPassword = errors.New("wrong password")
	ErrWeakPassword  = errors.New("password not allowed")
)

// TooManyError is a sign-in refused for a while after too many wrong
// passwords.
type TooManyError struct{ Wait time.Duration }

func (e *TooManyError) Error() string {
	return fmt.Sprintf("too many wrong passwords; try again in %s", e.Wait.Round(time.Second))
}

// sessionTTL is how long a session lasts without use; each use extends it.
// touchEvery bounds how often a use is written down.
const (
	sessionTTL = 30 * 24 * time.Hour
	touchEvery = time.Hour
)

// Service holds the password and the sessions.
type Service struct {
	st    *store.Store
	now   func() time.Time
	limit *limiter
	// hashing bounds the argon2id runs at once: each takes 64 MiB.
	hashing chan struct{}
	log     *slog.Logger
}

// New returns the service on st.
func New(st *store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{st: st, now: time.Now, limit: newLimiter(), hashing: make(chan struct{}, 2), log: log}
}

// PasswordSet reports whether the dashboard has a password yet.
func (s *Service) PasswordSet(ctx context.Context) (bool, error) {
	_, err := s.st.Queries().GetAuthPassword(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Setup sets the first password and starts a session. ErrPasswordSet once
// there is one.
func (s *Service) Setup(ctx context.Context, pw string) (string, error) {
	if err := validPassword(pw); err != nil {
		return "", err
	}
	hash, err := s.hash(ctx, pw)
	if err != nil {
		return "", err
	}
	var token string
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		n, err := q.InsertAuthPassword(ctx, store.InsertAuthPasswordParams{Hash: hash, SetAt: s.now().UnixMilli()})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrPasswordSet
		}
		token, err = s.issue(ctx, q)
		return err
	})
	if err == nil {
		s.log.Info("dashboard password set")
	}
	return token, err
}

// Login checks pw for client (its address) and starts a session.
func (s *Service) Login(ctx context.Context, client, pw string) (string, error) {
	if d := s.limit.wait(client, s.now()); d > 0 {
		return "", &TooManyError{Wait: d}
	}
	row, err := s.st.Queries().GetAuthPassword(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoPassword
	}
	if err != nil {
		return "", err
	}
	ok, err := s.check(ctx, row.Hash, pw)
	if err != nil {
		return "", err
	}
	if !ok {
		s.limit.fail(client, s.now())
		s.log.Warn("dashboard sign-in refused: wrong password", "client", client)
		return "", ErrWrongPassword
	}
	s.limit.succeed(client)
	var token string
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.PruneAuthSessions(ctx, s.now().UnixMilli()); err != nil {
			return err
		}
		token, err = s.issue(ctx, q)
		return err
	})
	return token, err
}

// Logout ends the session token belongs to.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.st.Queries().DeleteAuthSession(ctx, hashToken(token))
}

// Valid reports whether token is a live session, and extends it.
func (s *Service) Valid(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	h := hashToken(token)
	row, err := s.st.Queries().GetAuthSession(ctx, h)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := s.now()
	if now.UnixMilli() >= row.ExpiresAt {
		_ = s.st.Queries().DeleteAuthSession(ctx, h)
		return false, nil
	}
	if now.Sub(time.UnixMilli(row.LastSeen)) >= touchEvery {
		if err := s.st.Queries().TouchAuthSession(ctx, store.TouchAuthSessionParams{
			LastSeen: now.UnixMilli(), ExpiresAt: now.Add(sessionTTL).UnixMilli(), TokenHash: h,
		}); err != nil {
			s.log.Warn("cannot extend a dashboard session", "error", err)
		}
	}
	return true, nil
}

// ChangePassword checks current, sets pw and ends every session; it
// returns a new session for the caller.
func (s *Service) ChangePassword(ctx context.Context, client, current, pw string) (string, error) {
	if d := s.limit.wait(client, s.now()); d > 0 {
		return "", &TooManyError{Wait: d}
	}
	row, err := s.st.Queries().GetAuthPassword(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoPassword
	}
	if err != nil {
		return "", err
	}
	ok, err := s.check(ctx, row.Hash, current)
	if err != nil {
		return "", err
	}
	if !ok {
		s.limit.fail(client, s.now())
		return "", ErrWrongPassword
	}
	var token string
	err = s.set(ctx, pw, func(q *store.Queries) error {
		var err error
		token, err = s.issue(ctx, q)
		return err
	})
	return token, err
}

// SetPassword sets pw without the current one, and ends every session. Only
// the CLI's socket offers it.
func (s *Service) SetPassword(ctx context.Context, pw string) error {
	return s.set(ctx, pw, nil)
}

// Reset removes the password and every session: the next visitor to the
// dashboard sets a new one.
func (s *Service) Reset(ctx context.Context) error {
	err := s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteAuthSessions(ctx); err != nil {
			return err
		}
		return q.DeleteAuthPassword(ctx)
	})
	if err == nil {
		s.log.Info("dashboard password removed; the next visitor sets one")
	}
	return err
}

func (s *Service) set(ctx context.Context, pw string, then func(*store.Queries) error) error {
	if err := validPassword(pw); err != nil {
		return err
	}
	hash, err := s.hash(ctx, pw)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetAuthPassword(ctx, store.SetAuthPasswordParams{Hash: hash, SetAt: s.now().UnixMilli()}); err != nil {
			return err
		}
		if err := q.DeleteAuthSessions(ctx); err != nil {
			return err
		}
		if then != nil {
			return then(q)
		}
		return nil
	})
	if err == nil {
		s.log.Info("dashboard password changed; every session ended")
	}
	return err
}

// issue starts a session and returns its token.
func (s *Service) issue(ctx context.Context, q *store.Queries) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	err := q.InsertAuthSession(ctx, store.InsertAuthSessionParams{
		TokenHash: hashToken(token), CreatedAt: now.UnixMilli(), LastSeen: now.UnixMilli(),
		ExpiresAt: now.Add(sessionTTL).UnixMilli(),
	})
	return token, err
}

func (s *Service) hash(ctx context.Context, pw string) (string, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer func() { <-s.hashing }()
	return hashPassword(pw)
}

func (s *Service) check(ctx context.Context, hash, pw string) (bool, error) {
	if err := s.acquire(ctx); err != nil {
		return false, err
	}
	defer func() { <-s.hashing }()
	return checkPassword(hash, pw)
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.hashing <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
