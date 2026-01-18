package registry

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type Device struct {
	IP        string    `json:"ip"`
	MAC       string    `json:"mac"`
	ShardID   string    `json:"shard_id"`
	Online    bool      `json:"online"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// Enrichment (best-effort, no-credentials)
	Vendor      string `json:"vendor,omitempty"`
	Model       string `json:"model,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Worker      string `json:"worker,omitempty"`
	UptimeS     uint64 `json:"uptime_s,omitempty"`
	HashrateTHS float64 `json:"hashrate_ths,omitempty"`
	OpenPorts   []int  `json:"open_ports,omitempty"`
	Confidence  int    `json:"confidence,omitempty"` // 0..100
}

type Store struct {
	mu   sync.RWMutex
	byIP map[string]*Device

	subMu sync.Mutex
	subs  map[int64]chan struct{}
	subID atomic.Int64
}

func NewStore() *Store {
	return &Store{
		byIP: map[string]*Device{},
		subs: map[int64]chan struct{}{},
	}
}

func (s *Store) UpsertDiscovery(shardID, ip, mac string, now time.Time) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()

	d := s.byIP[ip]
	if d == nil {
		d = &Device{IP: ip, MAC: mac, ShardID: shardID, FirstSeen: now}
		s.byIP[ip] = d
	}
	if mac != "" {
		d.MAC = mac
	}
	if shardID != "" {
		d.ShardID = shardID
	}
	d.LastSeen = now

	s.notifyLocked()
	return d
}

func (s *Store) UpdateEnrichment(ip string, fn func(d *Device)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.byIP[ip]
	if d == nil {
		return
	}
	fn(d)
	d.LastSeen = time.Now().UTC()
	s.notifyLocked()
}

func (s *Store) UpsertObserved(shardID, ip, mac string, online bool, now time.Time) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()

	d := s.byIP[ip]
	if d == nil {
		d = &Device{IP: ip, MAC: mac, ShardID: shardID, FirstSeen: now}
		s.byIP[ip] = d
	}
	if mac != "" {
		d.MAC = mac
	}
	if shardID != "" {
		d.ShardID = shardID
	}
	d.Online = online
	d.LastSeen = now

	s.notifyLocked()
	return d
}

func (s *Store) List() []*Device {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Device, 0, len(s.byIP))
	for _, d := range s.byIP {
		cp := *d
		out = append(out, &cp)
	}
	return out
}

// Subscribe emits a signal (coalesced) when the store changes.
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

func (s *Store) notifyLocked() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
			// drop (coalesce)
		}
	}
}

