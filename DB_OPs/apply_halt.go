package DB_OPs

import "fmt"

// applyHaltKey records why block apply is halted on this node (see
// BlockProcessing.ErrApplyHalted). It lives in the node's own sync KV, so it
// survives a restart and goes away with a wipe of the node's store.
const applyHaltKey = "block_apply_halt"

// RecordApplyHalt records that block apply is halted, and why.
func RecordApplyHalt(reason string) error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("record apply halt: %w", err)
	}
	if err := h.PutSyncKV(applyHaltKey, []byte(reason)); err != nil {
		return fmt.Errorf("record apply halt: %w", err)
	}
	return nil
}

// RecordedApplyHalt returns the recorded halt reason, or "" when none is.
func RecordedApplyHalt() (string, error) {
	h, err := getHandle(nil)
	if err != nil {
		return "", fmt.Errorf("read apply halt: %w", err)
	}
	raw, err := h.GetSyncKV(applyHaltKey)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read apply halt: %w", err)
	}
	return string(raw), nil
}

// ClearRecordedApplyHalt erases the halt record.
func ClearRecordedApplyHalt() error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("clear apply halt: %w", err)
	}
	if err := h.PutSyncKV(applyHaltKey, []byte{}); err != nil {
		return fmt.Errorf("clear apply halt: %w", err)
	}
	return nil
}
