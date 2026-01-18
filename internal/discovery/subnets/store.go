package subnets

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"asic-control/internal/netutil"
)

type Subnet struct {
	ID         int64     `json:"id"`
	CIDR       string    `json:"cidr"`
	Enabled    bool      `json:"enabled"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	// runtime
	Scanning   bool      `json:"scanning"`
	LastScanAt time.Time `json:"last_scan_at"`
	Progress   int       `json:"progress"` // 0..100 best-effort
}

type Store struct {
	mu      sync.RWMutex
	byID    map[int64]*Subnet
	nextID  atomic.Int64

	subMu sync.Mutex
	subs  map[int64]chan struct{}
	subID atomic.Int64
}

func NewStore() *Store {
	return &Store{
		byID: map[int64]*Subnet{},
		subs: map[int64]chan struct{}{},
	}
}

func (s *Store) Add(cidr string) (*Subnet, error) {
	return s.AddWithNote(cidr, "")
}

func (s *Store) AddWithNote(spec string, note string) (*Subnet, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("cidr is empty")
	}
	// Accept CIDR or manual ranges (spec).
	if strings.Contains(spec, "/") {
		if _, _, err := net.ParseCIDR(spec); err != nil {
			return nil, err
		}
	} else {
		p := netutil.PreviewSpec(spec)
		if !p.Valid {
			return nil, errors.New(p.Error)
		}
	}

	now := time.Now().UTC()
	id := s.nextID.Add(1)
	sub := &Subnet{
		ID:        id,
		CIDR:      spec,
		Enabled:   true,
		Note:      strings.TrimSpace(note),
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.mu.Lock()
	s.byID[id] = sub
	s.mu.Unlock()
	s.notify()

	return cloneSubnet(sub), nil
}

func (s *Store) Delete(id int64) bool {
	s.mu.Lock()
	_, ok := s.byID[id]
	if ok {
		delete(s.byID, id)
	}
	s.mu.Unlock()
	if ok {
		s.notify()
	}
	return ok
}

func (s *Store) Get(id int64) (*Subnet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return cloneSubnet(sub), true
}

func (s *Store) List() []*Subnet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Subnet, 0, len(s.byID))
	for _, sub := range s.byID {
		out = append(out, cloneSubnet(sub))
	}
	return out
}

func (s *Store) SetEnabled(id int64, enabled bool) {
	s.mu.Lock()
	sub, ok := s.byID[id]
	if ok {
		sub.Enabled = enabled
		sub.UpdatedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	if ok {
		s.notify()
	}
}

func (s *Store) SetScanState(id int64, scanning bool, progress int, lastScanAt time.Time) {
	s.mu.Lock()
	sub, ok := s.byID[id]
	if ok {
		sub.Scanning = scanning
		sub.Progress = progress
		if !lastScanAt.IsZero() {
			sub.LastScanAt = lastScanAt
		}
		sub.UpdatedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	if ok {
		s.notify()
	}
}

func (s *Store) Subscribe(ctx context.Context) <-chan struct{} {
	id := s.subID.Add(1)
	ch := make(chan struct{}, 1)

	s.subMu.Lock()
	s.subs[id] = ch
	s.subMu.Unlock()

	go func() {
		<-ctx.Done()
		s.subMu.Lock()
		delete(s.subs, id)
		close(ch)
		s.subMu.Unlock()
	}()

	return ch
}

func (s *Store) notify() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func cloneSubnet(in *Subnet) *Subnet {
	cp := *in
	return &cp
}

