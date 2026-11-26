package MessagePassing

import "encoding/json"

// abstainNotSyncedReply is the abstain reply when nothing more can be said.
const abstainNotSyncedReply = `{"error":"node not synced; abstaining from consensus"}`

// abstainNotSyncedMessage builds the reply a node sends when it abstains
// because it is not ready to vote (consensusVoteReady false: slot clock not
// recovered, or not synced, typically right after a restart).
//
// When the request names a block height and this node has a signing record at
// it, the reply carries highest_period, exactly as a refusal does. The
// sequencer already reads that field from every vote-result reply
// (Sequencer.parseVoteResultResponse -> ProposerPeriods.NoteRefusal), so a
// round where seated nodes are still starting up still tells the proposer
// which periods are used, instead of coming back with no hint at all.
//
// The record is read from the durable ledgers (roundlock), which do not depend
// on the readiness this node lacks, so reporting it is truthful. The proposer
// only counts the (f+1)-th largest report, so one wrong report cannot move it.
func abstainNotSyncedMessage(request string) string {
	reply := map[string]any{"error": "node not synced; abstaining from consensus"}
	var req struct {
		BlockNumber uint64 `json:"block_number"`
	}
	if err := json.Unmarshal([]byte(request), &req); err == nil && req.BlockNumber > 0 {
		if hp, ok := highestRecordedPeriod(req.BlockNumber); ok {
			reply["highest_period"] = hp
		}
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return abstainNotSyncedReply
	}
	return string(raw)
}
