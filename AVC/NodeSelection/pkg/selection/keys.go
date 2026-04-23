package selection

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tyler-smith/go-bip39"
)

// GenerateKeysFromMnemonic generates ed25519 keys from a mnemonic phrase
func GenerateKeysFromMnemonic(mnemonic string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	// Trim and normalize whitespace
	mnemonic = strings.TrimSpace(mnemonic)

	// Validate mnemonic
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, nil, fmt.Errorf("invalid mnemonic phrase")
	}

	// Generate seed from mnemonic (empty passphrase)
	seed := bip39.NewSeed(mnemonic, "")

	// Derive ed25519 key from seed
	hash := sha512.Sum512(seed)
	privateKey := ed25519.NewKeyFromSeed(hash[:32])
	publicKey := privateKey.Public().(ed25519.PublicKey)

	return publicKey, privateKey, nil
}

// GenerateNewMnemonic creates a new random mnemonic
func GenerateNewMnemonic() (string, error) {
	entropy, err := bip39.NewEntropy(256) // 24 words
	if err != nil {
		return "", err
	}

	return bip39.NewMnemonic(entropy)
}

// LoadKeysFromConfig loads keys based on environment configuration
func LoadKeysFromConfig(privateKeyHex, mnemonic string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	// Priority 1: Use PRIVATE_KEY if provided
	if privateKeyHex != "" {
		privateKeyBytes, err := hex.DecodeString(strings.TrimSpace(privateKeyHex))
		if err != nil {
			return nil, nil, fmt.Errorf("invalid PRIVATE_KEY hex: %w", err)
		}

		if len(privateKeyBytes) != ed25519.PrivateKeySize {
			return nil, nil, fmt.Errorf("invalid PRIVATE_KEY size: expected %d, got %d",
				ed25519.PrivateKeySize, len(privateKeyBytes))
		}

		privateKey := ed25519.PrivateKey(privateKeyBytes)
		publicKey := privateKey.Public().(ed25519.PublicKey)

		return publicKey, privateKey, nil
	}

	// Priority 2: Use MNEMONIC if provided
	if mnemonic != "" {
		return GenerateKeysFromMnemonic(mnemonic)
	}

	return nil, nil, fmt.Errorf("no PRIVATE_KEY or MNEMONIC provided")
}
