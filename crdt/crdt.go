package crdt

import (
    "encoding/json"
    "fmt"
    // "time"
    
    "gossipnode/DB_OPs"
)

// CRDT is an interface for conflict-free replicated data types
type CRDT interface {
    // GetKey returns the unique identifier for this CRDT
    GetKey() string
    
    // GetTimestamp returns the vector timestamp of this CRDT
    GetTimestamp() VectorClock
    
    // Merge combines this CRDT with another one and returns the result
    Merge(other CRDT) (CRDT, error)
}

// VectorClock represents a vector clock for tracking causality
type VectorClock map[string]uint64

// Compare compares two vector clocks and returns:
//   -1 if vc is less than other (happens-before)
//    0 if vc is concurrent with other
//    1 if vc is greater than other (happens-after)
func (vc VectorClock) Compare(other VectorClock) int {
    less := false
    greater := false
    
    // Check entries in our clock
    for node, timestamp := range vc {
        otherTimestamp, exists := other[node]
        if !exists {
            greater = true
        } else if timestamp > otherTimestamp {
            greater = true
        } else if timestamp < otherTimestamp {
            less = true
        }
    }
    
    // Check for entries in other clock that we don't have
    for node := range other {
        if _, exists := vc[node]; !exists {
            less = true
        }
    }
    
    // Determine relationship
    if less && greater {
        return 0 // Concurrent changes
    } else if greater {
        return 1 // Happens-after
    } else if less {
        return -1 // Happens-before
    }
    return 0 // Equal clocks
}

// Merge combines two vector clocks, taking the maximum value for each node
func (vc VectorClock) Merge(other VectorClock) VectorClock {
    result := make(VectorClock)
    
    // Copy our clock
    for node, ts := range vc {
        result[node] = ts
    }
    
    // Merge with other clock, taking max values
    for node, otherTS := range other {
        if ourTS, exists := result[node]; !exists || otherTS > ourTS {
            result[node] = otherTS
        }
    }
    
    return result
}

// Increment increases the counter for the specified node
func (vc VectorClock) Increment(nodeID string) {
    if current, exists := vc[nodeID]; exists {
        vc[nodeID] = current + 1
    } else {
        vc[nodeID] = 1
    }
}

// LWWSet implements a Last-Writer-Wins Set CRDT
type LWWSet struct {
    Key       string                 `json:"key"`
    Adds      map[string]VectorClock `json:"adds"`
    Removes   map[string]VectorClock `json:"removes"`
    Timestamp VectorClock            `json:"timestamp"`
}

// NewLWWSet creates a new Last-Writer-Wins Set
func NewLWWSet(key string) *LWWSet {
    return &LWWSet{
        Key:       key,
        Adds:      make(map[string]VectorClock),
        Removes:   make(map[string]VectorClock),
        Timestamp: make(VectorClock),
    }
}

// GetKey returns the unique identifier for this CRDT
func (s *LWWSet) GetKey() string {
    return s.Key
}

// GetTimestamp returns the vector timestamp of this CRDT
func (s *LWWSet) GetTimestamp() VectorClock {
    return s.Timestamp
}

// Add adds an element to the set
func (s *LWWSet) Add(nodeID, element string) {
    // Clone and increment timestamp
    ts := make(VectorClock)
    for k, v := range s.Timestamp {
        ts[k] = v
    }
    ts.Increment(nodeID)
    
    // Add element with new timestamp
    s.Adds[element] = ts
    
    // Update CRDT timestamp
    s.Timestamp = ts
}

// Remove removes an element from the set
func (s *LWWSet) Remove(nodeID, element string) {
    // Clone and increment timestamp
    ts := make(VectorClock)
    for k, v := range s.Timestamp {
        ts[k] = v
    }
    ts.Increment(nodeID)
    
    // Mark element as removed with new timestamp
    s.Removes[element] = ts
    
    // Update CRDT timestamp
    s.Timestamp = ts
}

// Contains checks if an element is in the set
func (s *LWWSet) Contains(element string) bool {
    addTS, addExists := s.Adds[element]
    removeTS, removeExists := s.Removes[element]
    
    // If element was never added, it's not in the set
    if !addExists {
        return false
    }
    
    // If element was never removed, it's in the set
    if !removeExists {
        return true
    }
    
    // Compare timestamps - removal wins in case of tie
    return addTS.Compare(removeTS) > 0
}

// GetElements returns all elements currently in the set
func (s *LWWSet) GetElements() []string {
    var elements []string
    
    for element := range s.Adds {
        if s.Contains(element) {
            elements = append(elements, element)
        }
    }
    
    return elements
}

// Merge combines this CRDT with another one
func (s *LWWSet) Merge(other CRDT) (CRDT, error) {
    otherSet, ok := other.(*LWWSet)
    if !ok {
        return nil, fmt.Errorf("can't merge different CRDT types")
    }
    
    if s.Key != otherSet.Key {
        return nil, fmt.Errorf("can't merge sets with different keys")
    }
    
    // Create a new set for the result
    result := NewLWWSet(s.Key)
    
    // Merge adds
    for element, ts := range s.Adds {
        result.Adds[element] = ts
    }
    for element, ts := range otherSet.Adds {
        if ourTS, exists := result.Adds[element]; !exists || ts.Compare(ourTS) != -1 {
            result.Adds[element] = ts
        }
    }
    
    // Merge removes
    for element, ts := range s.Removes {
        result.Removes[element] = ts
    }
    for element, ts := range otherSet.Removes {
        if ourTS, exists := result.Removes[element]; !exists || ts.Compare(ourTS) != -1 {
            result.Removes[element] = ts
        }
    }
    
    // Merge timestamps
    result.Timestamp = s.Timestamp.Merge(otherSet.Timestamp)
    
    return result, nil
}

// Counter implements a Grow-Only Counter CRDT
type Counter struct {
    Key       string              `json:"key"`
    Counters  map[string]uint64   `json:"counters"`
    Timestamp VectorClock         `json:"timestamp"`
}

// NewCounter creates a new Counter CRDT
func NewCounter(key string) *Counter {
    return &Counter{
        Key:       key,
        Counters:  make(map[string]uint64),
        Timestamp: make(VectorClock),
    }
}

// GetKey returns the unique identifier for this CRDT
func (c *Counter) GetKey() string {
    return c.Key
}

// GetTimestamp returns the vector timestamp of this CRDT
func (c *Counter) GetTimestamp() VectorClock {
    return c.Timestamp
}

// Increment increases the counter for the specified node
func (c *Counter) Increment(nodeID string, value uint64) {
    if current, exists := c.Counters[nodeID]; exists {
        c.Counters[nodeID] = current + value
    } else {
        c.Counters[nodeID] = value
    }
    
    c.Timestamp.Increment(nodeID)
}

// Value returns the current value of the counter
func (c *Counter) Value() uint64 {
    var total uint64
    for _, value := range c.Counters {
        total += value
    }
    return total
}

// Merge combines this CRDT with another one
func (c *Counter) Merge(other CRDT) (CRDT, error) {
    otherCounter, ok := other.(*Counter)
    if !ok {
        return nil, fmt.Errorf("can't merge different CRDT types")
    }
    
    if c.Key != otherCounter.Key {
        return nil, fmt.Errorf("can't merge counters with different keys")
    }
    
    // Create a new counter for the result
    result := NewCounter(c.Key)
    
    // Merge counters by taking max values
    for nodeID, value := range c.Counters {
        result.Counters[nodeID] = value
    }
    for nodeID, value := range otherCounter.Counters {
        if ourValue, exists := result.Counters[nodeID]; !exists || value > ourValue {
            result.Counters[nodeID] = value
        }
    }
    
    // Merge timestamps
    result.Timestamp = c.Timestamp.Merge(otherCounter.Timestamp)
    
    return result, nil
}

// Engine manages CRDT operations and persistence
type Engine struct {
    db *DB_OPs.ImmuClient
}

// NewEngine creates a new CRDT engine
func NewEngine(db *DB_OPs.ImmuClient) *Engine {
    return &Engine{
        db: db,
    }
}

// IsCRDT checks if a key represents a CRDT
func (e *Engine) IsCRDT(key string) bool {
    // Check if key has our CRDT prefix
    return len(key) > 6 && key[:5] == "crdt:"
}

// StoreCRDT persists a CRDT to the database
func (e *Engine) StoreCRDT(crdt CRDT) error {
    // Serialize the CRDT
    data, err := json.Marshal(crdt)
    if err != nil {
        return fmt.Errorf("failed to serialize CRDT: %w", err)
    }
    
    // Store with CRDT prefix
    key := "crdt:" + crdt.GetKey()
    return e.db.Create(key, data)
}

// LoadCRDT retrieves a CRDT from the database
func (e *Engine) LoadCRDT(key string) (CRDT, error) {
    // Ensure key has prefix
    if !e.IsCRDT(key) {
        key = "crdt:" + key
    }
    
    // Retrieve data
    data, err := e.db.Read(key)
    if err != nil {
        return nil, fmt.Errorf("failed to read CRDT: %w", err)
    }
    
    // Determine CRDT type and deserialize
    return e.DeserializeCRDT(key, data, nil)
}

// DeserializeCRDT deserializes CRDT data based on the key
func (e *Engine) DeserializeCRDT(key string, data []byte, target CRDT) (CRDT, error) {
    // Extract base key without prefix
    baseKey := key
    if e.IsCRDT(key) {
        baseKey = key[5:]
    }
    
    // Check key pattern to determine type
    var result CRDT
    
    // If target is provided, use its type
    if target != nil {
        result = target
    } else if len(baseKey) > 4 && baseKey[:4] == "set:" {
        result = &LWWSet{}
    } else if len(baseKey) > 8 && baseKey[:8] == "counter:" {
        result = &Counter{}
    } else {
        // Default to LWWSet
        result = &LWWSet{}
    }
    
    // Deserialize
    if err := json.Unmarshal(data, result); err != nil {
        return nil, fmt.Errorf("failed to deserialize CRDT: %w", err)
    }
    
    return result, nil
}

// MergeCRDT merges a CRDT with existing one in database
func (e *Engine) MergeCRDT(incoming CRDT) (CRDT, error) {
    key := "crdt:" + incoming.GetKey()
    
    // Try to load existing CRDT
    existing, err := e.LoadCRDT(key)
    if err != nil {
        // If not found, just return the incoming CRDT
        if err.Error() == "key not found" {
            return incoming, nil
        }
        return nil, err
    }
    
    // Merge CRDTs
    merged, err := existing.Merge(incoming)
    if err != nil {
        return nil, fmt.Errorf("failed to merge CRDTs: %w", err)
    }
    
    return merged, nil
}

// CreateLWWSet creates and stores a new LWWSet
func (e *Engine) CreateLWWSet(key string) (*LWWSet, error) {
    set := NewLWWSet(key)
    if err := e.StoreCRDT(set); err != nil {
        return nil, err
    }
    return set, nil
}

// CreateCounter creates and stores a new Counter
func (e *Engine) CreateCounter(key string) (*Counter, error) {
    counter := NewCounter(key)
    if err := e.StoreCRDT(counter); err != nil {
        return nil, err
    }
    return counter, nil
}