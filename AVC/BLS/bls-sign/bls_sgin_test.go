package blssign

import (
	"testing"

	"go.dedis.ch/dela/crypto/bls"
)

func testKey(t *testing.T) (priv, pub []byte) {
	t.Helper()
	s := bls.Generate().(bls.Signer)
	priv, err := s.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	pub, err = s.GetPublicKey().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

// A-6 (2026-10-22 audit): the identity key with the identity signature
// verified every message, and trailing bytes after a valid key or signature
// were ignored.
func TestBLSVerifyRejectsIdentityAndOverlongEncodings(t *testing.T) {
	idPub := make([]byte, PublicKeyLen)
	idSig := make([]byte, SignatureLen)
	if BLSVerify(idPub, []byte("anything"), idSig) == nil {
		t.Fatal("identity key and signature verified an arbitrary message")
	}

	priv, pub := testKey(t)
	msg := []byte("m")
	sig, err := BLSSign(priv, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != PublicKeyLen || len(sig) != SignatureLen {
		t.Fatalf("encoded sizes changed: pub=%d sig=%d", len(pub), len(sig))
	}
	if err := BLSVerify(pub, msg, sig); err != nil {
		t.Fatalf("a valid signature must still verify: %v", err)
	}
	for name, c := range map[string]struct{ pub, sig []byte }{
		"key with trailing bytes":       {append(append([]byte{}, pub...), 0xde, 0xad), sig},
		"signature with trailing bytes": {pub, append(append([]byte{}, sig...), 0xde, 0xad)},
		"identity signature":            {pub, idSig},
		"identity key":                  {idPub, sig},
	} {
		if BLSVerify(c.pub, msg, c.sig) == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestBLSFastAggregateVerify(t *testing.T) {
	msg := []byte("block")
	var pubs, sigs [][]byte
	for i := 0; i < 3; i++ {
		priv, pub := testKey(t)
		sig, err := BLSSign(priv, msg)
		if err != nil {
			t.Fatal(err)
		}
		pubs, sigs = append(pubs, pub), append(sigs, sig)
	}
	agg, err := BLSAggregate(sigs...)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := BLSFastAggregateVerify(pubs, msg, agg); !ok || err != nil {
		t.Fatalf("a valid aggregate must verify: ok=%v err=%v", ok, err)
	}
	idPub := make([]byte, PublicKeyLen)
	if ok, _ := BLSFastAggregateVerify([][]byte{idPub}, msg, make([]byte, SignatureLen)); ok {
		t.Fatal("an identity-only aggregate verified")
	}
}
