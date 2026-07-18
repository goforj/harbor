// Package dnsserver serves Harbor's exact, IPv4-only .test records.
package dnsserver

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const (
	// MinTTL prevents record churn from disabling useful resolver caching entirely.
	MinTTL = time.Second
	// MaxTTL bounds how long removed Harbor endpoints can remain in resolver caches.
	MaxTTL = 5 * time.Minute
	// DefaultTTL balances local endpoint changes with ordinary resolver traffic.
	DefaultTTL = 30 * time.Second
)

// Record maps one canonical, exact .test name to an IPv4 loopback address.
type Record struct {
	// Name is the canonical lowercase host without a trailing root dot.
	Name string
	// Address is the Harbor-owned IPv4 loopback identity published for Name.
	Address netip.Addr
}

// Snapshot is an immutable candidate record set ready for atomic publication.
type Snapshot struct {
	ttl     uint32
	records map[string]netip.Addr
	ordered []Record
	valid   bool
}

// NewSnapshot validates and copies a complete candidate record set.
func NewSnapshot(records []Record, ttl time.Duration) (Snapshot, error) {
	ttlSeconds, err := validateTTL(ttl)
	if err != nil {
		return Snapshot{}, err
	}

	byName := make(map[string]netip.Addr, len(records))
	ordered := make([]Record, 0, len(records))
	for index, record := range records {
		if err := validateRecord(record); err != nil {
			return Snapshot{}, fmt.Errorf("dns snapshot: record %d: %w", index, err)
		}
		if _, duplicate := byName[record.Name]; duplicate {
			return Snapshot{}, fmt.Errorf("dns snapshot: duplicate name %q", record.Name)
		}
		address := record.Address.Unmap()
		byName[record.Name] = address
		ordered = append(ordered, Record{Name: record.Name, Address: address})
	}

	sort.Slice(ordered, func(left int, right int) bool {
		return ordered[left].Name < ordered[right].Name
	})

	return Snapshot{
		ttl:     ttlSeconds,
		records: byName,
		ordered: ordered,
		valid:   true,
	}, nil
}

// TTL returns the positive-record cache lifetime represented by the snapshot.
func (s Snapshot) TTL() time.Duration {
	return time.Duration(s.ttl) * time.Second
}

// Records returns a canonical copy that callers can inspect without mutating the snapshot.
func (s Snapshot) Records() []Record {
	return append([]Record(nil), s.ordered...)
}

// validate rejects zero values and protects publication from corrupted package-local snapshots.
func (s Snapshot) validate() error {
	if !s.valid {
		return fmt.Errorf("dns snapshot: initialize the snapshot with NewSnapshot")
	}
	if _, err := validateTTL(time.Duration(s.ttl) * time.Second); err != nil {
		return err
	}
	if len(s.records) != len(s.ordered) {
		return fmt.Errorf("dns snapshot: record indexes are inconsistent")
	}
	for index, record := range s.ordered {
		if err := validateRecord(record); err != nil {
			return fmt.Errorf("dns snapshot: record %d: %w", index, err)
		}
		address, exists := s.records[record.Name]
		if !exists || address != record.Address {
			return fmt.Errorf("dns snapshot: record index for %q is inconsistent", record.Name)
		}
	}
	return nil
}

// validateTTL keeps both positive answers and later record removal operationally bounded.
func validateTTL(ttl time.Duration) (uint32, error) {
	if ttl < MinTTL || ttl > MaxTTL {
		return 0, fmt.Errorf("dns snapshot: TTL must be between %s and %s", MinTTL, MaxTTL)
	}
	if ttl%time.Second != 0 {
		return 0, fmt.Errorf("dns snapshot: TTL must use whole seconds")
	}
	return uint32(ttl / time.Second), nil
}

// validateRecord restricts DNS publication to exact Harbor-owned IPv4 loopback endpoints.
func validateRecord(record Record) error {
	if err := ValidateName(record.Name); err != nil {
		return err
	}
	address := record.Address.Unmap()
	if !address.IsValid() || !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("address %s is not IPv4 loopback", record.Address)
	}
	return nil
}

// ValidateName enforces the canonical exact-host form shared by durable reservations, DNS, and certificate SANs.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 253 {
		return fmt.Errorf("name %q exceeds 253 bytes", name)
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("name %q must be lowercase", name)
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("name %q must not include a trailing dot", name)
	}
	if !strings.HasSuffix(name, ".test") {
		return fmt.Errorf("name %q is outside the .test zone", name)
	}

	for _, label := range strings.Split(name, ".") {
		if err := validateLabel(label); err != nil {
			return fmt.Errorf("name %q: %w", name, err)
		}
	}
	return nil
}

// validateLabel accepts the portable ASCII hostname subset used by Harbor-generated domains.
func validateLabel(label string) error {
	if label == "" || len(label) > 63 {
		return fmt.Errorf("each label must contain between 1 and 63 bytes")
	}
	for index := range len(label) {
		character := label[index]
		alphanumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if !alphanumeric && character != '-' {
			return fmt.Errorf("label %q contains unsupported character %q", label, character)
		}
		if character == '-' && (index == 0 || index == len(label)-1) {
			return fmt.Errorf("label %q must not start or end with a hyphen", label)
		}
	}
	return nil
}
