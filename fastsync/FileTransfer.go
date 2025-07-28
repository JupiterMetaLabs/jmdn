package fastsync

import (
	"fmt"
	"gossipnode/transfer"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TransferBAKFile(h host.Host, peerID peer.ID, filepath string) error {
	// Debugging
	fmt.Println("Transferring BAK file to peer:", peerID.String())
	fmt.Println("Filepath:", filepath)
	fmt.Println("File size:", filepath)	

	
	err := transfer.SendFile(h, peerID, filepath)
	if err != nil {
		return fmt.Errorf("failed to send file: %w", err)
	}
	return nil
}