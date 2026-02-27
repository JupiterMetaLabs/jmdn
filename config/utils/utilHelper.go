package utils

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/multiformats/go-multiaddr"
)

//
// -------- Big Int <-> String --------
//

// StrToBigIntBase converts a string to *big.Int using the provided base.
func StrToBigIntBase(s string, base int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty string for big.Int")
	}
	z := new(big.Int)
	if _, ok := z.SetString(s, base); !ok {
		return nil, fmt.Errorf("invalid big.Int %q (base %d)", s, base)
	}
	return z, nil
}

// BigIntToStrBase converts *big.Int to string in the provided base.
func BigIntToStrBase(x *big.Int, base int) string {
	if x == nil {
		return ""
	}
	return x.Text(base)
}

//
// -------- Uint64 <-> String --------
//

// StrToUint64 parses s into uint64. If base==0, auto-detects 0x prefix as hex.
func StrToUint64(s string, base int) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty string for uint64")
	}

	switch base {
	case 0: // auto
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			return strconv.ParseUint(s[2:], 16, 64)
		}
		return strconv.ParseUint(s, 10, 64)
	case 16:
		s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
		return strconv.ParseUint(s, 16, 64)
	default:
		return strconv.ParseUint(s, base, 64)
	}
}

// Uint64ToStrBase formats a uint64 in the specified base (0x prefix when base=16).
func Uint64ToStrBase(u uint64, base int) string {
	switch base {
	case 10:
		return strconv.FormatUint(u, 10)
	case 16:
		return "0x" + strconv.FormatUint(u, 16)
	default:
		return strconv.FormatUint(u, base)
	}
}

//
// -------- Bytes <-> String (hex/base64/utf8) --------
//

// HexToBytes accepts "0x" prefixed or plain hex. Pads odd-length hex automatically.
func HexToBytes(s string) ([]byte, error) {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "0x"), "0X")
	if len(t)%2 == 1 {
		t = "0" + t
	}
	return hex.DecodeString(t)
}

// BytesToHex returns lower-case hex; set with0x to include "0x" prefix.
func BytesToHex(b []byte, with0x bool) string {
	h := hex.EncodeToString(b)
	if with0x {
		return "0x" + h
	}
	return h
}

// Base64ToBytes decodes standard base64.
func Base64ToBytes(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// BytesToBase64 encodes standard base64.
func BytesToBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// UTF8ToBytes returns UTF-8 bytes for the string (identity for Go strings).
func UTF8ToBytes(s string) []byte { return []byte(s) }

// BytesToUTF8 converts bytes to string assuming UTF-8.
func BytesToUTF8(b []byte) string { return string(b) }

// StrToBytesAuto tries hex (0x or valid hex), then base64 (heuristic), else UTF-8.
func StrToBytesAuto(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []byte{}, nil
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") || isLikelyHex(s) {
		if b, err := HexToBytes(s); err == nil {
			return b, nil
		}
	}
	if looksBase64(s) {
		if b, err := Base64ToBytes(s); err == nil {
			return b, nil
		}
	}
	return UTF8ToBytes(s), nil
}

func isLikelyHex(s string) bool {
	t := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if t == "" {
		return false
	}
	_, err := hex.DecodeString(evenHex(t))
	return err == nil
}

func evenHex(h string) string {
	if len(h)%2 == 1 {
		return "0" + h
	}
	return h
}

func looksBase64(s string) bool {
	if len(s) < 4 || len(s)%4 != 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '/' && r != '=' {
			return false
		}
	}
	return true
}

//
// -------- Address/DID helpers --------
//

// IsDID returns true if s looks like a DID (prefix "did:" per spec).
func IsDID(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "did:")
}

// Conversion of []common.Hash to [][]byte
func ConvertHashesToByteArrays(hashes []common.Hash) [][]byte {
	byteArrays := make([][]byte, len(hashes))
	for i, hash := range hashes {
		byteArrays[i] = hash.Bytes()
	}
	return byteArrays
}

// GenerateReceiptRoot generates a receipt root from a list of receipts
// This follows Ethereum's receipt root calculation using RLP encoding and Keccak-256
func GenerateReceiptRoot(receipts []*config.Receipt) ([]byte, error) {
	if len(receipts) == 0 {
		// Empty receipts list - return empty hash
		return make([]byte, 32), nil
	}

	// RLP encode the receipts
	receiptBytes, err := rlp.EncodeToBytes(receipts)
	if err != nil {
		return nil, fmt.Errorf("failed to RLP encode receipts: %w", err)
	}

	// Calculate Keccak-256 hash of the RLP encoded receipts
	hash := crypto.Keccak256(receiptBytes)

	return hash, nil
}

// GenerateReceiptRootAsHash generates a receipt root and returns it as common.Hash
func GenerateReceiptRootAsHash(receipts []*config.Receipt) (common.Hash, error) {
	rootBytes, err := GenerateReceiptRoot(receipts)
	if err != nil {
		return common.Hash{}, err
	}

	var hash common.Hash
	copy(hash[:], rootBytes)
	return hash, nil
}

// Get the Multiaddr of the host
func GetMultiAddrs(host host.Host) []multiaddr.Multiaddr {
	list := make([]multiaddr.Multiaddr, 0)
	for _, addr := range host.Addrs() {
		ma, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", addr.String(), host.ID().String()))
		if err == nil {
			list = append(list, ma)
		}
	}
	return list
}
