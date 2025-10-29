package CRDTSync

import (
	"gossipnode/AVC/BuddyNodes/DataLayer"
	"log"
)

// PrintCurrentCRDTContent prints all CRDT elements after sync
// This is called after CRDT sync and before vote aggregation
func PrintCurrentCRDTContent() {
	// Get the CRDT layer
	crdtLayer := DataLayer.GetCRDTLayer()
	if crdtLayer == nil || crdtLayer.CRDTLayer == nil {
		log.Printf("⚠️ CRDT layer not initialized, skipping print")
		return
	}

	// Get all CRDT objects
	allCRDTs := crdtLayer.CRDTLayer.GetAllCRDTs()

	log.Printf("\n" + "═══════════════════════════════════════════════════════════")
	log.Printf("📊 CRDT SYNC SUMMARY - READY FOR VOTE AGGREGATION")
	log.Printf("═══════════════════════════════════════════════════════════")
	log.Printf("📦 Total CRDT Objects: %d", len(allCRDTs))
	log.Printf("═══════════════════════════════════════════════════════════\n")

	if len(allCRDTs) == 0 {
		log.Printf("📋 No CRDT objects found (empty CRDT)")
		log.Printf("═══════════════════════════════════════════════════════════\n")
		return
	}

	// Print each CRDT with details
	for key, crdt := range allCRDTs {
		log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		log.Printf("📋 CRDT Key: %s", key)
		log.Printf("   Type: %T", crdt)
		log.Printf("   Data: %+v", crdt)
	}

	log.Printf("═══════════════════════════════════════════════════════════")
	log.Printf("✅ CRDT sync complete - Ready for vote aggregation\n")
}
