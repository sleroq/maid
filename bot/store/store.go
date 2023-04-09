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
	Users map[string]UserData
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

	if _, ok := s.Chats[roomID]; !ok {
		s.Chats[roomID] = &Chat{Users: map[string]UserData{}}
	}

	s.Chats[roomID].Users[userID] = data
}
