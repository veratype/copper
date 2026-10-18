package copper_dhcp

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Lease is one MAC→IP binding tracked by the server.
type Lease struct {
	MAC       string    `json:"mac"`
	IP        net.IP    `json:"ip"`
	ExpiresAt time.Time `json:"expires_at"`
	Reserved  bool      `json:"reserved"`
}

// MarshalJSON encodes IP as a dotted IPv4 string (not raw []byte / base64).
func (l Lease) MarshalJSON() ([]byte, error) {
	ip := ""
	if l.IP != nil {
		ip = l.IP.String()
	}
	return json.Marshal(struct {
		MAC       string    `json:"mac"`
		IP        string    `json:"ip"`
		ExpiresAt time.Time `json:"expires_at"`
		Reserved  bool      `json:"reserved"`
	}{
		MAC:       l.MAC,
		IP:        ip,
		ExpiresAt: l.ExpiresAt,
		Reserved:  l.Reserved,
	})
}

// UnmarshalJSON accepts IP as a dotted string (or legacy []byte / number array).
func (l *Lease) UnmarshalJSON(data []byte) error {
	var aux struct {
		MAC       string          `json:"mac"`
		IP        json.RawMessage `json:"ip"`
		ExpiresAt time.Time       `json:"expires_at"`
		Reserved  bool            `json:"reserved"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	l.MAC = aux.MAC
	l.ExpiresAt = aux.ExpiresAt
	l.Reserved = aux.Reserved
	if len(aux.IP) == 0 || string(aux.IP) == "null" {
		l.IP = nil
		return nil
	}
	var asStr string
	if err := json.Unmarshal(aux.IP, &asStr); err == nil {
		l.IP = net.ParseIP(asStr)
		if l.IP != nil {
			l.IP = l.IP.To4()
		}
		return nil
	}
	var asBytes []byte
	if err := json.Unmarshal(aux.IP, &asBytes); err == nil && len(asBytes) >= 4 {
		l.IP = net.IP(asBytes).To4()
		return nil
	}
	var asArr []int
	if err := json.Unmarshal(aux.IP, &asArr); err == nil && len(asArr) >= 4 {
		l.IP = net.IPv4(byte(asArr[0]), byte(asArr[1]), byte(asArr[2]), byte(asArr[3])).To4()
		return nil
	}
	return fmt.Errorf("invalid lease ip %s", string(aux.IP))
}

// Store is an in-memory lease table with pool allocation.
type Store struct {
	mu          sync.Mutex
	cfg         *Normalized
	byMAC       map[string]*Lease
	byIP        map[string]*Lease
	persistPath string // optional JSON file; empty disables persistence
}

func NewStore(cfg *Normalized) *Store {
	return &Store{
		cfg:   cfg,
		byMAC: map[string]*Lease{},
		byIP:  map[string]*Lease{},
	}
}

// Allocate picks an IP for mac (reservation or dynamic pool).
func (s *Store) Allocate(mac net.HardwareAddr, prefer net.IP) (net.IP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())

	key := macKey(mac)
	if key == "" {
		return nil, fmt.Errorf("empty mac")
	}

	if ip, ok := s.cfg.ReserveMAC[key]; ok {
		s.putLocked(key, ip, true)
		s.saveLocked()
		return append(net.IP(nil), ip...), nil
	}

	if prefer != nil {
		prefer = prefer.To4()
	}
	if prefer != nil && s.inPool(prefer) && s.ipFreeForLocked(prefer, key) {
		s.putLocked(key, prefer, false)
		s.saveLocked()
		return append(net.IP(nil), prefer...), nil
	}

	if existing, ok := s.byMAC[key]; ok && !existing.Reserved {
		s.putLocked(key, existing.IP, false)
		s.saveLocked()
		return append(net.IP(nil), existing.IP...), nil
	}

	for v := ipToUint(s.cfg.RangeStart); v <= ipToUint(s.cfg.RangeEnd); v++ {
		ip := uintToIP(v)
		if !s.ipFreeForLocked(ip, key) {
			continue
		}
		s.putLocked(key, ip, false)
		s.saveLocked()
		return append(net.IP(nil), ip...), nil
	}
	return nil, fmt.Errorf("dhcp pool exhausted")
}

// Confirm marks a Requested ACK binding (same as allocate with prefer).
func (s *Store) Confirm(mac net.HardwareAddr, ip net.IP) (net.IP, error) {
	if ip == nil {
		return s.Allocate(mac, nil)
	}
	return s.Allocate(mac, ip)
}

// Release frees a lease for mac.
func (s *Store) Release(mac net.HardwareAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := macKey(mac)
	lease, ok := s.byMAC[key]
	if !ok {
		return
	}
	if lease.Reserved {
		// Keep reservation slot occupied by this MAC conceptually; just refresh expiry drop.
		delete(s.byMAC, key)
		delete(s.byIP, lease.IP.String())
		s.saveLocked()
		return
	}
	delete(s.byMAC, key)
	delete(s.byIP, lease.IP.String())
	s.saveLocked()
}

// Decline frees an IP after client decline (and forgets MAC binding).
func (s *Store) Decline(mac net.HardwareAddr, ip net.IP) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := macKey(mac)
	if lease, ok := s.byMAC[key]; ok {
		delete(s.byMAC, key)
		delete(s.byIP, lease.IP.String())
	}
	if ip != nil {
		delete(s.byIP, ip.To4().String())
	}
	s.saveLocked()
}

// Leases returns a snapshot of active leases.
func (s *Store) Leases() []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.byMAC)
	s.expireLocked(time.Now())
	if len(s.byMAC) != before {
		s.saveLocked()
	}
	out := make([]Lease, 0, len(s.byMAC))
	for _, l := range s.byMAC {
		out = append(out, Lease{
			MAC:       l.MAC,
			IP:        append(net.IP(nil), l.IP...),
			ExpiresAt: l.ExpiresAt,
			Reserved:  l.Reserved,
		})
	}
	return out
}

func (s *Store) putLocked(mac string, ip net.IP, reserved bool) {
	if old, ok := s.byMAC[mac]; ok {
		delete(s.byIP, old.IP.String())
	}
	ip = append(net.IP(nil), ip.To4()...)
	lease := &Lease{
		MAC:       mac,
		IP:        ip,
		ExpiresAt: time.Now().Add(s.cfg.LeaseTime),
		Reserved:  reserved,
	}
	s.byMAC[mac] = lease
	s.byIP[ip.String()] = lease
}

func (s *Store) inPool(ip net.IP) bool {
	v := ipToUint(ip)
	return v >= ipToUint(s.cfg.RangeStart) && v <= ipToUint(s.cfg.RangeEnd)
}

func (s *Store) ipFreeForLocked(ip net.IP, mac string) bool {
	ip = ip.To4()
	if ip == nil {
		return false
	}
	// Reserved for another MAC?
	if owner, ok := s.cfg.ReserveIP[ip.String()]; ok && owner != mac {
		return false
	}
	if lease, ok := s.byIP[ip.String()]; ok && lease.MAC != mac {
		return false
	}
	return true
}

func (s *Store) expireLocked(now time.Time) {
	for mac, lease := range s.byMAC {
		if lease.ExpiresAt.Before(now) {
			delete(s.byMAC, mac)
			delete(s.byIP, lease.IP.String())
		}
	}
}
