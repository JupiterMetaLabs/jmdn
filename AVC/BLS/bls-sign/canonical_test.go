package blssign

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
)

// fieldP is the BN256 base-field prime (kyber pairing/bn256 `p`, unexported).
var fieldP, _ = new(big.Int).SetString("65000549695646603732796438742359905742825358107623003571877145026864184071783", 10)

// plusP returns enc with 32-byte coordinate i replaced by its value plus p, or
// false when that does not fit in 32 bytes.
func plusP(enc []byte, i int) ([]byte, bool) {
	c := new(big.Int).SetBytes(enc[32*i : 32*i+32])
	c.Add(c, fieldP)
	if c.BitLen() > 256 {
		return nil, false
	}
	out := append([]byte(nil), enc...)
	c.FillBytes(out[32*i : 32*i+32])
	return out, true
}

// x+p is the same G1 point under different bytes: rejected as a signature.
func TestBLSVerify_RejectsNonCanonicalSignature(t *testing.T) {
	priv, pub := testKey(t)
	for i := 0; i < 200; i++ {
		msg := []byte(fmt.Sprintf("canonical-%d", i))
		sig, err := BLSSign(priv, msg)
		if err != nil {
			t.Fatal(err)
		}
		alt, ok := plusP(sig, 0)
		if !ok {
			continue
		}
		a, b := suite.G1().Point(), suite.G1().Point()
		if a.UnmarshalBinary(sig) != nil || b.UnmarshalBinary(alt) != nil || !a.Equal(b) {
			t.Fatal("x+p must decode to the same point (otherwise this test proves nothing)")
		}
		if err := BLSVerify(pub, msg, sig); err != nil {
			t.Fatalf("honest signature must verify: %v", err)
		}
		if err := BLSVerify(pub, msg, alt); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("x+p re-encoding must be ErrBadSignature, got %v", err)
		}
		if _, err := BLSFastAggregateVerify([][]byte{pub}, msg, alt); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("x+p aggregate must be ErrBadSignature, got %v", err)
		}
		return
	}
	t.Fatal("no signature with room for x+p in 200 tries")
}

// The same holds for any coordinate of a G2 public key.
func TestBLSVerify_RejectsNonCanonicalPublicKey(t *testing.T) {
	msg := []byte("canonical-key")
	for i := 0; i < 50; i++ {
		priv, pub := testKey(t)
		for c := 0; c < 4; c++ {
			alt, ok := plusP(pub, c)
			if !ok {
				continue
			}
			sig, err := BLSSign(priv, msg)
			if err != nil {
				t.Fatal(err)
			}
			if err := BLSVerify(pub, msg, sig); err != nil {
				t.Fatalf("honest key must verify: %v", err)
			}
			if err := BLSVerify(alt, msg, sig); !errors.Is(err, ErrBadPublicKey) {
				t.Fatalf("+p re-encoded key must be ErrBadPublicKey, got %v", err)
			}
			return
		}
	}
	t.Fatal("no public key with room for +p in 50 tries")
}

func TestCanonical_HonestEncodingsAlwaysPass(t *testing.T) {
	for i := 0; i < 32; i++ {
		priv, pub := testKey(t)
		if err := checkPublicKey(pub); err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		sig, err := BLSSign(priv, []byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		if err := checkSignature(sig); err != nil {
			t.Fatalf("signature %d: %v", i, err)
		}
	}
}
