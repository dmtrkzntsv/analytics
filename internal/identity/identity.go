// Package identity implements Plausible-style cookieless visitor identity:
// hash(daily_salt, ip, user_agent, project). The salt is destroyed on
// rotation so cross-day linking is impossible (spec §5.4/§5.4a).
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const (
	saltKey     = "visitor_salt"
	saltTimeKey = "visitor_salt_rotated_at"
	rotateEvery = 24 * time.Hour
)

func VisitorHash(salt, ip, userAgent, project string) string {
	h := sha256.New()
	for _, part := range []string{salt, ip, userAgent, project} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type MetaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

type Salter struct {
	meta MetaStore
	now  func() time.Time

	mu        sync.Mutex
	salt      string
	rotatedAt time.Time
}

func NewSalter(m MetaStore, now func() time.Time) *Salter {
	return &Salter{meta: m, now: now}
}

func (s *Salter) Current(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salt == "" {
		if err := s.loadLocked(ctx); err != nil {
			return "", err
		}
	}
	if s.salt == "" || s.now().Sub(s.rotatedAt) >= rotateEvery {
		if err := s.rotateLocked(ctx); err != nil {
			return "", err
		}
	}
	return s.salt, nil
}

func (s *Salter) Rotate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked(ctx)
}

func (s *Salter) loadLocked(ctx context.Context) error {
	salt, err := s.meta.GetMeta(ctx, saltKey)
	if err != nil {
		return err
	}
	at, err := s.meta.GetMeta(ctx, saltTimeKey)
	if err != nil {
		return err
	}
	if salt != "" && at != "" {
		if ts, err := time.Parse(time.RFC3339, at); err == nil {
			s.salt, s.rotatedAt = salt, ts
		}
	}
	return nil
}

func (s *Salter) rotateLocked(ctx context.Context) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("identity: entropy: %w", err)
	}
	salt := hex.EncodeToString(buf)
	if err := s.meta.SetMeta(ctx, saltKey, salt); err != nil {
		return err
	}
	if err := s.meta.SetMeta(ctx, saltTimeKey, s.now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	s.salt, s.rotatedAt = salt, s.now()
	return nil
}
