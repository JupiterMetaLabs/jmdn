package thebestatus

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync"
	"time"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
)

// Status mirrors Thebe runtime head/projection health.
type Status struct {
	KVHead       uint64
	SQLProjected uint64
	Lag          int64
	Uptime       string
	Mode         string
}

var (
	mu      sync.RWMutex
	dbRef   *thebedb.ThebeDB
	started time.Time
	mode    = "production"
)

// Register stores the active ThebeDB handle used for status reads.
func Register(db *thebedb.ThebeDB, runMode string) {
	mu.Lock()
	defer mu.Unlock()
	dbRef = db
	started = time.Now().UTC()
	if runMode != "" {
		mode = runMode
	}
}

// StatusFromCurrentDB returns status from the registered ThebeDB instance.
func StatusFromCurrentDB(ctx context.Context) (Status, error) {
	mu.RLock()
	db := dbRef
	st := started
	m := mode
	mu.RUnlock()

	if db == nil {
		return Status{}, fmt.Errorf("thebedb is not registered")
	}
	return StatusFromDB(ctx, db, st, m)
}

// StatusFromDB computes status values from ThebeDB KV + SQL heads.
func StatusFromDB(ctx context.Context, db *thebedb.ThebeDB, startedAt time.Time, runMode string) (Status, error) {
	if db == nil {
		return Status{}, fmt.Errorf("nil thebedb handle")
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if runMode == "" {
		runMode = "production"
	}

	kvHead := bestEffortKVHead(db)
	sqlHead, err := sqlProjectedHead(ctx, db)
	if err != nil {
		return Status{}, err
	}

	lag := int64(kvHead) - int64(sqlHead)
	if lag < 0 {
		lag = 0
	}

	return Status{
		KVHead:       kvHead,
		SQLProjected: sqlHead,
		Lag:          lag,
		Uptime:       time.Since(startedAt).String(),
		Mode:         runMode,
	}, nil
}

func sqlProjectedHead(ctx context.Context, db *thebedb.ThebeDB) (uint64, error) {
	row := db.SQL.GetDB().QueryRowContext(ctx, `
		SELECT last_seq
		FROM __sys_projection_state
		ORDER BY id DESC
		LIMIT 1`)
	var last sql.NullInt64
	if err := row.Scan(&last); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("query projection head: %w", err)
	}
	if !last.Valid || last.Int64 < 0 {
		return 0, nil
	}
	return uint64(last.Int64), nil
}

// bestEffortKVHead tries known KV APIs, then falls back to reflection.
func bestEffortKVHead(db *thebedb.ThebeDB) uint64 {
	kv := reflect.ValueOf(db.KV)
	if !kv.IsValid() {
		return 0
	}

	// Common fast path: LastSeq() (uint64, error) or (int64, error)
	if m := kv.MethodByName("LastSeq"); m.IsValid() {
		if out := m.Call(nil); len(out) >= 1 {
			if n, ok := toUint64(out[0]); ok {
				return n
			}
		}
	}

	// Requested path: Iterate(...) to discover max sequence.
	// Signature varies by backend, so this is best effort.
	if m := kv.MethodByName("Iterate"); m.IsValid() {
		mt := m.Type()
		if mt.NumIn() == 1 {
			cbType := mt.In(0)
			if cbType.Kind() == reflect.Func && cbType.NumIn() == 1 {
				var max uint64
				cb := reflect.MakeFunc(cbType, func(args []reflect.Value) []reflect.Value {
					if seq, ok := seqFromRecord(args[0]); ok && seq > max {
						max = seq
					}
					// Respect callback return type(s) if any.
					outs := make([]reflect.Value, cbType.NumOut())
					for i := 0; i < cbType.NumOut(); i++ {
						outs[i] = reflect.Zero(cbType.Out(i))
					}
					return outs
				})
				_ = m.Call([]reflect.Value{cb})
				return max
			}
		}
	}

	return 0
}

func seqFromRecord(v reflect.Value) (uint64, bool) {
	if !v.IsValid() {
		return 0, false
	}
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if !v.IsValid() {
		return 0, false
	}
	if v.Kind() == reflect.Struct {
		for _, name := range []string{"Seq", "Sequence", "ID", "Id"} {
			f := v.FieldByName(name)
			if f.IsValid() {
				if n, ok := toUint64(f); ok {
					return n, true
				}
			}
		}
	}
	return 0, false
}

func toUint64(v reflect.Value) (uint64, bool) {
	if !v.IsValid() {
		return 0, false
	}
	if v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := v.Int()
		if i < 0 {
			return 0, false
		}
		return uint64(i), true
	default:
		return 0, false
	}
}
