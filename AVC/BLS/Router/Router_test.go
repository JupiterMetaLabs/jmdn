package Router

import (
	"encoding/hex"
	"fmt"
	"testing"
)

func TestBLSRouter_MultinodeVotingAndAggregation(t *testing.T) {
	numNodes := 10
	numPlusOne := 6
	numMinusOne := 4

	if numPlusOne+numMinusOne != numNodes {
		t.Fatalf("Number of +1 and -1 voters doesn't add up to total nodes")
	}

	fmt.Printf("CREATING %d SIGNERS...\n", numNodes)
	// Create router and nodes
	router := NewBLSRouter()
	signerIDs := make([]string, 0, numNodes)
	privKeys := make(map[string][]byte)
	voteMap := make(map[string]int)

	for i := 0; i < numNodes; i++ {
		signerID := fmt.Sprintf("node_%d", i)
		sk, pk, err := GenerateKeyPair()
		if err != nil {
			t.Fatalf("Failed to generate keypair for %s: %v", signerID, err)
		}
		fmt.Printf("  Created signer: %s\n", signerID)
		privKeys[signerID] = sk
		if err := router.AddSigner(signerID, pk, sk); err != nil {
			t.Fatalf("Failed to add signer %s: %v", signerID, err)
		}
		signerIDs = append(signerIDs, signerID)
	}

	messageKey := "vote-session-001"
	voteMessage := "ConsensusVote"

	votes := make(map[string]int) // +1 or -1 per signer
	fmt.Printf("ASSIGNING VOTES...\n")
	for i, signerID := range signerIDs {
		vote := -1
		if i < numPlusOne {
			vote = 1
		}
		votes[signerID] = vote
		voteMap[signerID] = vote
		fmt.Printf("  %s vote = %d\n", signerID, vote)
	}

	fmt.Printf("SIGNING INDIVIDUAL VOTES...\n")
	for _, signerID := range signerIDs {
		vote := votes[signerID]
		msg := fmt.Sprintf("%s:%s:%d", voteMessage, signerID, vote)
		sig, err := router.bls.SignAsParticipant(signerID, []byte(msg))
		if err != nil {
			t.Fatalf("Signer %s failed to sign: %v", signerID, err)
		}
		fmt.Printf("  [SIGN] %s signs: msg=\"%s\"\n    signature=%s\n", signerID, msg, hex.EncodeToString(sig))
		signer, err := router.bls.GetSigner(signerID)
		if err != nil {
			t.Fatalf("Failed to retrieve signer %s: %v", signerID, err)
		}
		// The router should manage shares itself, but we'll call the API for directness
		err = router.bls.AddSignatureShare(messageKey, struct {
			SignerID  string
			PublicKey []byte
			Signature []byte
			Message   []byte
		}{
			SignerID:  signerID,
			PublicKey: signer.PublicKey,
			Signature: sig,
			Message:   []byte(msg),
		})
		if err != nil {
			t.Fatalf("Failed to add signature share for %s: %v", signerID, err)
		}
		fmt.Printf("    SignatureShare: {Signer=%s, PubKey=%s..., Sig=%s..., Msg=\"%s\"}\n",
			signerID,
			hex.EncodeToString(signer.PublicKey)[:8],
			hex.EncodeToString(sig)[:16],
			msg,
		)
		fmt.Printf("  Vote signed and share recorded: %s\n", signerID)
	}

	// Aggregate all +1 votes
	plusOneSigners := make([]string, 0, numPlusOne)
	fmt.Printf("COLLECTING +1 VOTERS FOR AGGREGATION...\n")
	for i, signerID := range signerIDs {
		if i < numPlusOne {
			plusOneSigners = append(plusOneSigners, signerID)
			fmt.Printf("  +1 voter: %s\n", signerID)
		}
	}

	resultMsg := fmt.Sprintf("Result: majority = %d", 1) // +1 is the majority
	fmt.Printf("SIGNING AGGREGATED RESULT MESSAGE BY MAJORITY...\n")
	for _, signerID := range plusOneSigners {
		sig, err := router.bls.SignAsParticipant(signerID, []byte(resultMsg))
		if err != nil {
			t.Fatalf("Result signing failed for %s: %v", signerID, err)
		}
		signer, err := router.bls.GetSigner(signerID)
		if err != nil {
			t.Fatalf("No signer %s: %v", signerID, err)
		}
		err = router.bls.AddSignatureShare("result-majority", struct {
			SignerID  string
			PublicKey []byte
			Signature []byte
			Message   []byte
		}{
			SignerID:  signerID,
			PublicKey: signer.PublicKey,
			Signature: sig,
			Message:   []byte(resultMsg),
		})
		if err != nil {
			t.Fatalf("Failed to add majority signature: %v", err)
		}
		fmt.Printf("  [RESULT SIGN] %s signs result: msg=\"%s\"\n    signature=%s\n", signerID, resultMsg, hex.EncodeToString(sig))
		fmt.Printf("    SignatureShare: {Signer=%s, PubKey=%s..., Sig=%s..., Msg=\"%s\"}\n",
			signerID,
			hex.EncodeToString(signer.PublicKey)[:8],
			hex.EncodeToString(sig)[:16],
			resultMsg,
		)
		fmt.Printf("  Result signed by: %s\n", signerID)
	}

	fmt.Printf("AGGREGATING MAJORITY SIGNATURES...\n")
	aggSig, pubKeys, _, err := router.bls.AggregateSignatures("result-majority")
	if err != nil {
		t.Fatalf("Failed to aggregate: %v", err)
	}
	fmt.Printf("Aggregated Signature: %s\n", hex.EncodeToString(aggSig))
	fmt.Printf("Aggregated from public keys: %d\n", len(pubKeys))

	fmt.Printf("VERIFYING AGGREGATED SIGNATURE...\n")
	ok, err := router.bls.VerifyAggregatedSignature("result-majority", aggSig)
	if err != nil {
		t.Fatalf("Error in aggregate verify: %v", err)
	}
	if !ok {
		t.Errorf("Aggregated signature failed verification!")
	} else {
		fmt.Printf("Verification: PASSED\n")
	}

	t.Logf("Aggregate verified: majority (+1) signatures accepted: agg = %s", hex.EncodeToString(aggSig))
}
