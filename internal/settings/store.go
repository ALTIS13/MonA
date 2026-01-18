package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	mu   sync.RWMutex
	path string
	cur  Settings
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = "data"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "settings.json")

	s := &Store{path: path, cur: Defaults()}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

func (s *Store) Update(newS Settings) error {
	s.mu.Lock()
	s.cur = newS
	s.mu.Unlock()
	return s.save()
}

func (s *Store) Patch(fn func(*Settings)) error {
	s.mu.Lock()
	cp := s.cur
	fn(&cp)
	s.cur = cp
	s.mu.Unlock()
	return s.save()
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.save()
		}
		return err
	}
	var cfg Settings
	if err := json.Unmarshal(b, &cfg); err != nil {
		// keep defaults if corrupt
		return nil
	}
	if cfg.Version == 0 {
		// old/unknown -> keep defaults, overwrite file
		return s.save()
	}
	s.mu.Lock()
	s.cur = cfg
	s.mu.Unlock()
	return nil
}

func (s *Store) save() error {
	s.mu.RLock()
	cfg := s.cur
	s.mu.RUnlock()

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
