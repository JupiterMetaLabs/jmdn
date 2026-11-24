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
	"github.com/JupiterMetaLabs/avc/vdf"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
)

// PersistVDFProof durably records a verified proof for an epoch.
//
// Non-fatal by design, like PersistEpochEntropy: failing to store the proof
// costs this node the ability to SERVE that epoch to a peer later. It does not
// affect this node's own entropy, which is already published, so it must never
// fail a block path or take down a sealing goroutine.
//
// D-43: the proof is stored in its own encoding, not as the bytes the block
// carried. vdf.Proof decodes with json.Unmarshal, which ignores unknown
// fields, so a proposer could pad a valid proof past DB_OPs.MaxVDFProofBytes:
// every node adopted it, failed to store it, and then answered Found:false to
// every peer recovering that epoch. Re-encoding drops the padding and gives
// the same bytes the sealer stores (it persists MarshalBinary's output).
func PersistVDFProof(epoch uint64, encodedProof []byte) error {
	if len(encodedProof) == 0 {
		return nil
	}
	encodedProof = canonicalVDFProofEncoding(encodedProof)
	// Proves entropy sealed from an unconfirmed last-resort mix: held in
	// memory, out of durable storage with that entropy, and still served by
	// LookupVDFProof (see heldLastResortProofs).
	if holdIfSealedFromLastResortMix(epoch, encodedProof) {
		return nil
	}
	if err := recordVDFProofFn(nil, epoch, encodedProof); err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).
			Msg("entropy: failed to persist the VDF proof — this node will be unable to serve this " +
				"epoch's proof to a recovering peer, and cannot re-derive it once the mix ages out")
		return err
	}
	releaseHeldLastResortProof(epoch)
	return nil
}

// recordVDFProofFn and getVDFProofFn are DB_OPs.RecordVDFProof and
// DB_OPs.GetVDFProof; variables so tests need no DB.
var (
	recordVDFProofFn = DB_OPs.RecordVDFProof
	getVDFProofFn    = DB_OPs.GetVDFProof
)

// canonicalVDFProofEncoding returns MarshalBinary's encoding of the proof
// encoded, or encoded unchanged if it does not decode. Callers pass only
// verified proofs, which always decode.
func canonicalVDFProofEncoding(encoded []byte) []byte {
	var p vdf.Proof
	if err := p.UnmarshalBinary(encoded); err != nil {
		return encoded
	}
	out, err := p.MarshalBinary()
	if err != nil {
		return encoded
	}
	return out
}

// LookupVDFProof returns the stored proof encoding for an epoch, or the proof
// held in memory while its last-resort mix is unconfirmed.
//
// Used by the proof-request responder and by Sequencer.SealerResultFor once
// the sealer is gone. Returns ok=false for "not stored", which is a normal
// answer, not an error: most epochs are not boundary epochs this node sealed
// or adopted.
func LookupVDFProof(epoch uint64) (encoded []byte, ok bool) {
	raw, found, err := getVDFProofFn(nil, epoch)
	if err != nil {
		log.Warn().Err(err).Uint64("epoch", epoch).
			Msg("entropy: reading the persisted VDF proof failed — answering the request as not-found")
		return heldLastResortProof(epoch)
	}
	if !found || len(raw) == 0 {
		return heldLastResortProof(epoch)
	}
	return raw, true
}
