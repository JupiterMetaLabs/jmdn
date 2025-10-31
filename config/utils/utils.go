package utils

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gossipnode/config"
	"os"
	"sync"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	cachedPrivKey crypto.PrivKey
	cachedPubKey  crypto.PubKey
	privKeyOnce   sync.Once
	pubKeyOnce    sync.Once
)

// loadPrivateKeyFromFile loads an existing private key and peer ID from ./config/peer.json.
// It does NOT generate a new key if the file or key is missing.
func loadPrivateKeyFromFile() (crypto.PrivKey, peer.ID, error) {
	// Check if peer.json exists
	if _, err := os.Stat(config.PeerFile); err != nil {
		return nil, "", fmt.Errorf("peer.json not found: %w", err)
	}

	// Open the file
	file, err := os.Open(config.PeerFile)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open peer.json: %v", err)
	}
	defer file.Close()

	// Decode JSON
	var cfg config.PeerConfig
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, "", fmt.Errorf("failed to decode peer.json: %v", err)
	}

	if cfg.PrivKeyB64 == "" {
		return nil, "", fmt.Errorf("priv_key missing in peer.json")
	}

	// Decode and unmarshal private key
	privKeyBytes, err := base64.StdEncoding.DecodeString(cfg.PrivKeyB64)
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode private key: %v", err)
	}

	privKey, err := crypto.UnmarshalPrivateKey(privKeyBytes)
	if err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal private key: %v", err)
	}

	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to derive peer ID: %v", err)
	}

	return privKey, peerID, nil
}

func ReturnPrivateKey() (crypto.PrivKey, error) {
	var err error
	privKeyOnce.Do(func() {
		cachedPrivKey, _, err = loadPrivateKeyFromFile()
	})
	return cachedPrivKey, err
}

func ReturnPublicKey() (crypto.PubKey, error) {
	var err error
	pubKeyOnce.Do(func() {
		privKey, e := ReturnPrivateKey()
		if e != nil {
			err = e
			return
		}
		cachedPubKey = privKey.GetPublic()
	})
	return cachedPubKey, err
}

func ReturnPublicKeyString() (string, error) {
	pubKey, err := ReturnPublicKey()
	if err != nil {
		return "", err
	}
	pubKeyBytes, err := pubKey.Raw()
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(pubKeyBytes), nil
}
