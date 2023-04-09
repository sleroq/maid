package fastore

import (
	"maunium.net/go/mautrix/id"
	"sync"
	"time"
)

type Challenge struct {
	Challenge     string
	Solution      int
	Expiry        time.Time
	Tries         int
	TaskMessageID id.EventID
}
type UserData struct {
	Challenge Challenge
	Verified  bool
}
type Chat struct {
	Users    map[string]UserData
	JoinedAt time.Time
}
type FastStore struct {
	Chats map[string]*Chat
	mu    sync.Mutex
}

// GetUserData Receives UserData from store
func (s *FastStore) GetUserData(roomID string, userID string) UserData {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Chats == nil {
		s.Chats = make(map[string]*Chat)
	}

	if _, ok := s.Chats[roomID]; !ok {
		s.Chats[roomID] = &Chat{Users: map[string]UserData{}}
	}

	return s.Chats[roomID].Users[userID]
}

// UpdateUserData Updates UserData in store
func (s *FastStore) UpdateUserData(roomID string, userID string, data UserData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Chats == nil {
		s.Chats = make(map[string]*Chat)
	}

	// Create chat if it doesn't exist, set JoinedAt in the past, so we won't ignore new joins
	if _, ok := s.Chats[roomID]; !ok {
		s.Chats[roomID] = &Chat{Users: map[string]UserData{}, JoinedAt: time.Now().Add(-time.Minute * 15)}
	}

	s.Chats[roomID].Users[userID] = data
}

// GetJoinedAt Returns time when user joined the room
func (s *FastStore) GetJoinedAt(roomID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Chats == nil {
		s.Chats = make(map[string]*Chat)
	}

	if _, ok := s.Chats[roomID]; !ok {
		s.Chats[roomID] = &Chat{Users: map[string]UserData{}}
	}

	return s.Chats[roomID].JoinedAt
}

// UpdateJoinedAt Updates time when user joined the room
func (s *FastStore) UpdateJoinedAt(roomID string, joinedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Chats == nil {
		s.Chats = make(map[string]*Chat)
	}

	if _, ok := s.Chats[roomID]; !ok {
		s.Chats[roomID] = &Chat{Users: map[string]UserData{}}
	}

	s.Chats[roomID].JoinedAt = joinedAt
}
