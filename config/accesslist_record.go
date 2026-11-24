package config

import "encoding/json"

// accessListRecordKey is the JSON object key the access list is stored under in
// the transactions.access_list JSONB column.
const accessListRecordKey = "tuples"

// ToRecord encodes the access list for the transactions.access_list JSONB
// column. An empty list returns nil (the column default applies). The access
// list is part of a type-1/type-2 transaction's signed contents, so it must
// survive storage for a stored transaction to re-hash to its tx hash and
// recover its sender.
func (al AccessList) ToRecord() map[string]any {
	if len(al) == 0 {
		return nil
	}
	raw, err := json.Marshal(al)
	if err != nil {
		return nil
	}
	var tuples []any
	if err := json.Unmarshal(raw, &tuples); err != nil {
		return nil
	}
	return map[string]any{accessListRecordKey: tuples}
}

// AccessListFromRecord decodes a value written by AccessList.ToRecord. A nil,
// empty or unrecognised record yields an empty list.
func AccessListFromRecord(rec map[string]any) AccessList {
	v, ok := rec[accessListRecordKey]
	if !ok || v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var al AccessList
	if err := json.Unmarshal(raw, &al); err != nil {
		return nil
	}
	if len(al) == 0 {
		return nil
	}
	return al
}
