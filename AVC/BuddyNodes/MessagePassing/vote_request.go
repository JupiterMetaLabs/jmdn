package MessagePassing

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// flexUint64 unmarshals from either a JSON number (13222) or a quoted string
// ("13222"). It exists because the 2026-07 halt was caused by a serialization
// mismatch: the sequencer sent block_number as a JSON string while the buddy's
// request struct declared a plain uint64, so json.Unmarshal failed and the buddy
// rejected every vote-result request (never signing). Accepting both makes the
// vote path resilient to that class of number/string drift across builds.
type flexUint64 uint64

func (f *flexUint64) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		*f = flexUint64(v)
		return nil
	}
	var v uint64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = flexUint64(v)
	return nil
}
