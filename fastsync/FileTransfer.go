package fastsync

import (
	"context"
	"fmt"
	"os"

	"gossipnode/transfer"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TransferAVROFile(h host.Host, peerID peer.ID, filepath string, remoteFilename string) error {
	// Debugging
	logger().Debug(context.Background(), "Transferring AVRO file to peer",
		ion.String("peer", peerID.String()),
		ion.String("filepath", filepath),
		ion.String("remote_filename", remoteFilename))

	// Check if file exists and has content
	fileInfo, err := os.Stat(filepath)
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", filepath, err)
	}

	// Skip transfer if file is empty
	if fileInfo.Size() == 0 {
		logger().Info(context.Background(), "Skipping empty file transfer",
			ion.String("peer", peerID.String()),
			ion.String("file", filepath))
		return nil
	}

	// Debug logging
	logger().Info(context.Background(), "Initiating file transfer",
		ion.String("peer", peerID.String()),
		ion.String("file", filepath),
		ion.Int64("size_bytes", fileInfo.Size()))

	err = transfer.SendFile(h, peerID, filepath, remoteFilename)
	if err != nil {
		return fmt.Errorf("failed to send file: %w", err)
	}

	logger().Info(context.Background(), "File transfer completed successfully",
		ion.String("peer", peerID.String()),
		ion.String("file", filepath))

	return nil
}
