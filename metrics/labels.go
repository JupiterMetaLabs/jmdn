package metrics

import (
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// Label hygiene for series keyed by remote input.
//
// Every distinct label value creates a series that lives until deleted. Peer
// IDs are free for a remote party to mint and message types are copied from
// the wire, so using them raw lets any peer grow the registry without bound.

// maxPeerLabels is how many distinct peer_id values are tracked at once. When
// a new peer pushes the set past this, the least recently seen peer's series
// are deleted from every peer-keyed metric.
const maxPeerLabels = 256

// OtherLabel replaces a label value outside the allowed set.
const OtherLabel = "other"

type partialDeleter interface {
	DeletePartialMatch(labels prometheus.Labels) int
}

var (
	peerLabelsOnce sync.Once
	peerLabels     *lru.Cache[string, struct{}]
)

func peerKeyedMetrics() []partialDeleter {
	return []partialDeleter{MessagesSentCounter, MessagesReceivedCounter, BlocksRejectedCounter, HeartbeatLatency}
}

func peerLabelCache() *lru.Cache[string, struct{}] {
	peerLabelsOnce.Do(func() {
		c, err := lru.NewWithEvict[string, struct{}](maxPeerLabels, func(id string, _ struct{}) {
			for _, m := range peerKeyedMetrics() {
				m.DeletePartialMatch(prometheus.Labels{"peer_id": id})
			}
		})
		if err != nil {
			panic(err) // only fails for a non-positive size
		}
		peerLabels = c
	})
	return peerLabels
}

// PeerLabel returns the peer_id label value for id and records it as recently
// seen, evicting the oldest tracked peer's series when the cap is exceeded.
func PeerLabel(id string) string {
	if id == "" {
		return OtherLabel
	}
	peerLabelCache().Add(id, struct{}{})
	return id
}

// knownProtocols are the protocol / message-type label values the node emits.
var knownProtocols = map[string]struct{}{
	"block": {}, "broadcast": {}, "message": {}, "contract": {}, "did": {}, "did_updated": {},
	"transactions": {}, "zkblock": {}, "zkblock_cert": {}, "vote_trigger": {},
	"timeout_vote": {}, "timeout_request": {}, "timeout_certificate": {},
}

// ProtocolLabel returns t when it is a known protocol / message type, and
// OtherLabel otherwise.
func ProtocolLabel(t string) string {
	if _, ok := knownProtocols[t]; ok {
		return t
	}
	return OtherLabel
}
