package camera

import (
	"fmt"
	"sync"
)

// Store holds cameras in memory (Phase 1 — persistent store later).
// Credentials are kept in a separate map so they are never accidentally
// included in a Cam returned to callers.
type Store struct {
	mu      sync.RWMutex
	cameras map[string]*Cam                // keyed by cam_id
	creds   map[string]*CamCredentialsWrite // keyed by cam_id
}

func NewStore() *Store {
	return &Store{
		cameras: make(map[string]*Cam),
		creds:   make(map[string]*CamCredentialsWrite),
	}
}

func (s *Store) Save(cam *Cam, cw *CamCredentialsWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cameras[cam.CamID]; exists {
		return fmt.Errorf("camera %q already exists", cam.CamID)
	}
	s.cameras[cam.CamID] = cam
	if cw != nil {
		s.creds[cam.CamID] = cw
	}
	return nil
}

func (s *Store) Get(camID string) (*Cam, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cam, ok := s.cameras[camID]
	return cam, ok
}

func (s *Store) List() []*Cam {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Cam, 0, len(s.cameras))
	for _, c := range s.cameras {
		out = append(out, c)
	}
	return out
}

func (s *Store) Delete(camID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cameras[camID]; !ok {
		return false
	}
	delete(s.cameras, camID)
	delete(s.creds, camID)
	return true
}
