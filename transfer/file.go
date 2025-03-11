package transfer

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	"gossipnode/metrics"
)

// HandleFileStream processes incoming files (QUIC)
func HandleFileStream(s network.Stream) {
    defer s.Close()
    startTime := time.Now()
    
    // Read file size
    header := make([]byte, 16)
    if _, err := io.ReadFull(s, header); err != nil {
        fmt.Println("Error reading header:", err)
        return
    }
    
    fileSize := binary.LittleEndian.Uint64(header)
    
    // Create output file
    outPath := fmt.Sprintf("received_%s", s.Conn().RemotePeer().String())
    file, err := os.Create(outPath)
    if err != nil {
        fmt.Println("Error creating file:", err)
        return
    }
    defer file.Close()
    
    // Use buffered reader and larger buffer
    bufReader := bufio.NewReaderSize(s, config.BufferSize)
    buffer := make([]byte, config.BufferSize)
    
    // Copy with large buffer
    bytesRead, err := io.CopyBuffer(file, bufReader, buffer)
    if err != nil {
        fmt.Println("Error receiving file:", err)
        return
    }
    
    // Calculate and display transfer speed
    elapsedTime := time.Since(startTime).Seconds()
    mbps := float64(bytesRead) / 1024 / 1024 / elapsedTime

    // Record file transfer metrics
    metrics.FileTransferDuration.WithLabelValues("sent", s.Conn().RemotePeer().String()).Observe(elapsedTime)
    metrics.FileTransferSpeedMBPS.WithLabelValues("sent", s.Conn().RemotePeer().String()).Observe(mbps)
    
    fmt.Printf("Received file (%d bytes) from %s saved as %s (%.2f MB/s)\n", 
        fileSize, s.Conn().RemotePeer().String(), file.Name(), mbps)
}

// SendFile sends a file to a peer (uses QUIC)
func SendFile(h host.Host, peerID peer.ID, filepath string) error {
    // Start timing
    startTime := time.Now()
    
    // Get file info
    fileInfo, err := os.Stat(filepath)
    if err != nil {
        return fmt.Errorf("file stat failed: %v", err)
    }
    fileSize := fileInfo.Size()
    
    // Open the file
    file, err := os.Open(filepath)
    if err != nil {
        return fmt.Errorf("file open failed: %v", err)
    }
    defer file.Close()
    
    // Open a stream with FileProtocol (QUIC)
    s, err := h.NewStream(context.Background(), peerID, config.FileProtocol)
    if err != nil {
        return fmt.Errorf("stream failed: %v", err)
    }
    defer s.Close()
    
    // Send file metadata (size)
    header := make([]byte, 16)
    binary.LittleEndian.PutUint64(header, uint64(fileSize))
    if _, err := s.Write(header); err != nil {
        return fmt.Errorf("header write failed: %v", err)
    }
    
    // Use buffered writer
    bufWriter := bufio.NewWriterSize(s, config.BufferSize)
    
    // Copy file with large buffer
    buffer := make([]byte, config.BufferSize)
    bytesWritten, err := io.CopyBuffer(bufWriter, file, buffer)
    if err != nil {
        return fmt.Errorf("send failed: %v", err)
    }
    
    // Flush buffered writer
    if err := bufWriter.Flush(); err != nil {
        return fmt.Errorf("flush failed: %v", err)
    }
    
    // Calculate and display transfer speed
    elapsedTime := time.Since(startTime).Seconds()
    mbps := float64(bytesWritten) / 1024 / 1024 / elapsedTime

    // Record file transfer metrics
    metrics.FileTransferDuration.WithLabelValues("sent", peerID.String()).Observe(elapsedTime)
    metrics.FileTransferSpeedMBPS.WithLabelValues("sent", peerID.String()).Observe(mbps)

 
    fmt.Printf("Sent file %s (%d bytes) to %s (%.2f MB/s)\n", 
        filepath, bytesWritten, peerID.String(), mbps)
    return nil
}