package DB_OPs

import (
	"strings"
	"testing"
)

func TestReplicationSlotWarnings(t *testing.T) {
	const cdcSlot = "thebe_eventlog"
	cases := []struct {
		name       string
		slots      []ReplicationSlot
		maxKeep    string
		cdcEnabled bool
		wantSlots  []string // slot names reported, in order
		wantCap    bool     // unlimited-cap warning expected
	}{
		{
			name:    "no slots, capped",
			maxKeep: "2GB",
		},
		{
			name:    "no slots, unlimited cap is still reported",
			maxKeep: UnlimitedSlotWALKeepSize,
			wantCap: true,
		},
		{
			name:      "CDC disabled leaves its slot behind",
			slots:     []ReplicationSlot{{Name: cdcSlot, HeldBytes: 5 << 30}},
			maxKeep:   "2GB",
			wantSlots: []string{cdcSlot},
		},
		{
			name:       "CDC enabled: its own inactive slot is about to be consumed",
			slots:      []ReplicationSlot{{Name: cdcSlot}},
			maxKeep:    "2GB",
			cdcEnabled: true,
		},
		{
			name:       "CDC enabled: a different inactive slot is reported",
			slots:      []ReplicationSlot{{Name: cdcSlot}, {Name: "old_slot"}},
			maxKeep:    "2GB",
			cdcEnabled: true,
			wantSlots:  []string{"old_slot"},
		},
		{
			name:      "active slots are never reported",
			slots:     []ReplicationSlot{{Name: "replica", Active: true}},
			maxKeep:   UnlimitedSlotWALKeepSize,
			wantCap:   true,
			wantSlots: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReplicationSlotWarnings(tc.slots, tc.maxKeep, tc.cdcEnabled, cdcSlot)
			var gotSlots []string
			gotCap := false
			for _, w := range got {
				if w.Slot == "" {
					gotCap = true
					if !strings.Contains(w.Message, "max_slot_wal_keep_size") {
						t.Errorf("cap warning message does not name the setting: %q", w.Message)
					}
					continue
				}
				gotSlots = append(gotSlots, w.Slot)
				if !strings.Contains(w.Message, "pg_drop_replication_slot('"+w.Slot+"')") {
					t.Errorf("slot warning for %q does not give the drop command: %q", w.Slot, w.Message)
				}
			}
			if strings.Join(gotSlots, ",") != strings.Join(tc.wantSlots, ",") {
				t.Errorf("reported slots = %v, want %v", gotSlots, tc.wantSlots)
			}
			if gotCap != tc.wantCap {
				t.Errorf("unlimited-cap warning = %v, want %v", gotCap, tc.wantCap)
			}
		})
	}
}

func TestReplicationSlotWarnings_CDCSlotMessage(t *testing.T) {
	got := ReplicationSlotWarnings([]ReplicationSlot{{Name: "thebe_eventlog"}}, "2GB", false, "thebe_eventlog")
	if len(got) != 1 || !strings.Contains(got[0].Message, "thebe.cdc.enabled is false") {
		t.Fatalf("want one warning naming thebe.cdc.enabled, got %+v", got)
	}
}
