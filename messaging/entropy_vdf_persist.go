package messaging

// Durable VDF proof storage, messaging-side.
//
// Companion to entropy_persist.go (which handles the entropy VALUE). This file
// handles the PROOF, which serves a different purpose: entropy is what this
// node needs to seat committees, whereas the proof is what a PEER needs to
// recover the epoch from us without re-running the VDF.
//
// Keyed by epoch rather than reachable only through the boundary block,
// because answering "give me the proof for epoch E" from block storage would
// need a slot->height index that does not exist — see DB_OPs/vdf_proof.go for
// why a chain scan is not an acceptable substitute in a request handler.
//
// Only VERIFIED proofs reach this file: one this node sealed itself, or one
// that already passed the five checks in VerifyAndAcceptVDFProof and was
// accepted by Pipeline.Accept.

import (
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
)

// PersistVDFProof durably records a verified proof for an epoch.
//
// Non-fatal by design, like PersistEpochEntropy: failing to store the proof
// costs this node the ability to SERVE that epoch to a peer later. It does not
// affect this node's own entropy, which is already published, so it must never
// fail a block path or take down a sealing goroutine.
func PersistVDFProof(epoch uint64, encodedProof []byte) error {
	if len(encodedProof) == 0 {
		return nil
	}
	if err := DB_OPs.RecordVDFProof(nil, epoch, encodedProof); err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).
			Msg("entropy: failed to persist the VDF proof — this node will be unable to serve this " +
				"epoch's proof to a recovering peer, and cannot re-derive it once the mix ages out")
		return err
	}
	return nil
}

// LookupVDFProof returns the stored proof encoding for an epoch.
//
// Used by the proof-request responder. Returns ok=false for "not stored",
// which is a normal answer, not an error: most epochs are not boundary epochs
// this node sealed or adopted.
func LookupVDFProof(epoch uint64) (encoded []byte, ok bool) {
	raw, found, err := DB_OPs.GetVDFProof(nil, epoch)
	if err != nil {
		log.Warn().Err(err).Uint64("epoch", epoch).
			Msg("entropy: reading the persisted VDF proof failed — answering the request as not-found")
		return nil, false
	}
	if !found || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}
