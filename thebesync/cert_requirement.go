package thebesync

import (
	"strings"
	"sync"

	"gossipnode/DB_OPs"
	"gossipnode/config/settings"
)

// The sync receiver's own "certificate seen" latch lives only for one session,
// so a fresh SyncFrom after a certified prefix would accept a cert-less block.
// certificateRequired makes the rule durable on this side:
//
//   - every block at or above consensus.sync_certificate_required_from (when
//     set) must carry a certificate — the activation height for a chain whose
//     early blocks predate certificates, and 1 for a chain certified from
//     genesis;
//   - every block whose parent was stored WITH a certificate must carry one
//     ("once certified, always certified"), read from the parent block itself,
//     which is persisted, so it survives restarts and new sessions.
var (
	lastAppliedMu     sync.Mutex
	lastAppliedHeight uint64
	lastAppliedCert   bool
	lastAppliedValid  bool
)

// noteApplied records the most recently applied block so the next block's
// parent lookup normally needs no storage read.
func noteApplied(height uint64, hadCert bool) {
	lastAppliedMu.Lock()
	lastAppliedHeight, lastAppliedCert, lastAppliedValid = height, hadCert, true
	lastAppliedMu.Unlock()
}

// certificateRequiredFrom is consensus.sync_certificate_required_from (0 = not
// set). A variable so tests can set it without a config file.
var certificateRequiredFrom = func() uint64 {
	if !settings.IsLoaded() {
		return 0
	}
	return settings.Get().Consensus.SyncCertificateRequiredFrom
}

// parentHadCertificate reports whether stored block `height` carries a
// committee certificate. A variable so tests can stub storage.
var parentHadCertificate = func(height uint64) (bool, error) {
	blk, err := DB_OPs.GetZKBlockByNumber(nil, height)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(blk.CommitteeCertificate) != "", nil
}

// certificateRequired reports whether block `height` (child of prevNumber)
// must carry a committee certificate. Fails closed: if the parent cannot be
// read, a certificate is required.
func certificateRequired(height, prevNumber uint64) bool {
	if from := certificateRequiredFrom(); from > 0 && height >= from {
		return true
	}
	if prevNumber == 0 {
		return false // parent is genesis, which never carries a certificate
	}
	lastAppliedMu.Lock()
	if lastAppliedValid && lastAppliedHeight == prevNumber {
		had := lastAppliedCert
		lastAppliedMu.Unlock()
		return had
	}
	lastAppliedMu.Unlock()
	had, err := parentHadCertificate(prevNumber)
	if err != nil {
		return true
	}
	return had
}
