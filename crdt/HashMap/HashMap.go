package Hashmap

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// HashMap represents a wrapper around a standard Go map[string]bool for public key or DID reconciliation.
type HashMap struct {
	Store map[string]bool `json:"store"`
}

// New creates a new empty HashMap.
func New() *HashMap {
	return &HashMap{
		Store: make(map[string]bool),
	}
}

// Insert adds a key to the map.
func (hm *HashMap) Insert(key string) {
	hm.Store[key] = true
}

// Exists checks if a key is present in the map.
func (hm *HashMap) Exists(key string) bool {
	return hm.Store[key]
}

// Delete removes a key from the map.
func (hm *HashMap) Delete(key string) {
	delete(hm.Store, key)
}

// Subtract returns a slice of keys present in hm but not in other.
func (hm *HashMap) Subtract(other *HashMap) []string {
	diff := make([]string, 0)
	for key := range hm.Store {
		if !other.Exists(key) {
			diff = append(diff, key)
		}
	}
	return diff
}

// Union returns a new HashMap containing keys from both input maps.
func (hm *HashMap) Union(other *HashMap) *HashMap {
	result := New()
	for k := range hm.Store {
		result.Insert(k)
	}
	for k := range other.Store {
		result.Insert(k)
	}
	return result
}

// ToJSON returns a compact JSON representation of the hashmap.
func (hm *HashMap) ToJSON() json.RawMessage {
	jsonBytes, err := json.Marshal(hm)
	if err != nil {
		return nil
	}
	return json.RawMessage(jsonBytes)
}

// Fingerprint returns a SHA256-based digest of sorted keys.
func (hm *HashMap) Fingerprint() string {
	keys := make([]string, 0, len(hm.Store))
	for k := range hm.Store {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Size returns the number of keys in the map.
func (hm *HashMap) Size() int {
	return len(hm.Store)
}

// Keys returns all keys in the hashmap.
func (hm *HashMap) Keys() []string {
	keys := make([]string, 0, len(hm.Store))
	for k := range hm.Store {
		keys = append(keys, k)
	}
	return keys
}
