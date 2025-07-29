package fastsync

import (
	"fmt"
	"gossipnode/transfer"
	"os"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"
)

func TransferBAKFile(h host.Host, peerID peer.ID, filepath string, remoteFilename string) error {
	// Debugging
	fmt.Println("Transferring BAK file to peer:", peerID.String())
	fmt.Println("Filepath:", filepath)
	fmt.Println("File name:", remoteFilename)	
	
	// Check if file exists and has content
	fileInfo, err := os.Stat(filepath)
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", filepath, err)
	}

	// Skip transfer if file is empty
	if fileInfo.Size() == 0 {
		log.Info().
			Str("peer", peerID.String()).
			Str("file", filepath).
			Msg("Skipping empty file transfer")
		return nil
	}

	// Debug logging
	log.Info().
		Str("peer", peerID.String()).
		Str("file", filepath).
		Int64("size_bytes", fileInfo.Size()).
		Msg("Initiating file transfer")

	err = transfer.SendFile(h, peerID, filepath, remoteFilename)
	if err != nil {
		return fmt.Errorf("failed to send file: %w", err)
	}

	log.Info().
		Str("peer", peerID.String()).
		Str("file", filepath).
		Msg("File transfer completed successfully")

	return nil
}