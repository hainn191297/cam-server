package auth

import (
	"fmt"
	"sync"
	"time"
)

// Store holds users and issued tokens in memory (Phase 1 — persistent store later).
// RBAC data (permissions, roles, user-role assignments) lives in RBACStore, not here.
type Store struct {
	mu     sync.RWMutex
	users  map[string]*User         // keyed by username
	byID   map[string]*User         // keyed by user_id
	tokens map[string]*SessionToken // keyed by token_id
}

func NewStore() *Store {
	return &Store{
		users:  make(map[string]*User),
		byID:   make(map[string]*User),
		tokens: make(map[string]*SessionToken),
	}
}

func (s *Store) AddUser(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[u.Username]; exists {
		return fmt.Errorf("user %q already exists", u.Username)
	}
	s.users[u.Username] = u
	s.byID[u.UserID] = u
	return nil
}

func (s *Store) GetByUsername(username string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	return u, ok
}

func (s *Store) GetByID(userID string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[userID]
	return u, ok
}

func (s *Store) ListUsers() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, u)
	}
	return out
}

func (s *Store) DeleteUser(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return false
	}
	delete(s.byID, userID)
	delete(s.users, u.Username)
	return true
}

func (s *Store) SaveToken(tok *SessionToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tok.TokenID] = tok
}

func (s *Store) GetToken(tokenID string) (*SessionToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tokens[tokenID]
	return t, ok
}

func (s *Store) InvalidateToken(tokenID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tokenID]
	if !ok || t.InvalidatedAt != nil {
		return false
	}
	now := time.Now()
	t.InvalidatedAt = &now
	return true
}

func (s *Store) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	now := time.Now()
	for _, t := range s.tokens {
		if t.InvalidatedAt == nil && t.ExpiresAt.After(now) {
			n++
		}
	}
	return n
}
