package transfer

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
	"gossipnode/metrics"
)

const (
	// Constants for buffer sizing algorithm
	minBufferSize     = 32 * 1024        // 32KB minimum buffer
	maxBufferSize     = 16 * 1024 * 1024 // 16MB maximum buffer
	baseBufferSize    = 256 * 1024       // 256KB base buffer size
	speedSampleWindow = 3                // Number of samples for moving average
	adjustmentFactor  = 0.8              // How aggressively to adjust (0.0-1.0)
	measureInterval   = 250 * time.Millisecond
)

// ConnectionStats tracks performance data for peer connections
type ConnectionStats struct {
	PeerID         string
	SpeedHistory   []float64 // MB/s
	BufferHistory  []int     // Buffer sizes used
	LastBufferSize int       // Last buffer size used
	LastSpeed      float64   // Last recorded speed
	ThroughputVar  float64   // Throughput variance
	RTT            time.Duration
	LastUpdated    time.Time
	mutex          sync.RWMutex
}

// Global map to store connection stats for each peer
var (
	peerConnStats = make(map[string]*ConnectionStats)
	statsMutex    sync.RWMutex
)

// addSpeedSample adds a new speed measurement and updates stats
func addSpeedSample(peerID string, speed float64, bufferSize int) {
	statsMutex.Lock()
	defer statsMutex.Unlock()

	stats, exists := peerConnStats[peerID]
	if !exists {
		stats = &ConnectionStats{
			PeerID:         peerID,
			SpeedHistory:   make([]float64, 0, speedSampleWindow),
			BufferHistory:  make([]int, 0, speedSampleWindow),
			LastBufferSize: bufferSize,
			LastSpeed:      speed,
			LastUpdated:    time.Now().UTC(),
		}
		peerConnStats[peerID] = stats
	}

	// Update stats with mutex protection
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	// Add new sample
	stats.SpeedHistory = append(stats.SpeedHistory, speed)
	stats.BufferHistory = append(stats.BufferHistory, bufferSize)

	// Keep only the most recent samples
	if len(stats.SpeedHistory) > speedSampleWindow {
		stats.SpeedHistory = stats.SpeedHistory[len(stats.SpeedHistory)-speedSampleWindow:]
		stats.BufferHistory = stats.BufferHistory[len(stats.BufferHistory)-speedSampleWindow:]
	}

	// Update variance if we have enough samples
	if len(stats.SpeedHistory) >= 2 {
		stats.ThroughputVar = calculateVariance(stats.SpeedHistory)
	}

	stats.LastSpeed = speed
	stats.LastBufferSize = bufferSize
	stats.LastUpdated = time.Now().UTC()
}

// calculateVariance computes the variance of the speed samples
func calculateVariance(samples []float64) float64 {
	if len(samples) < 2 {
		return 0
	}

	// Calculate mean
	var sum float64
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(len(samples))

	// Calculate variance
	var variance float64
	for _, s := range samples {
		diff := s - mean
		variance += diff * diff
	}
	return variance / float64(len(samples))
}

// getOptimalBufferSize calculates the optimal buffer size based on network conditions
func getOptimalBufferSize(peerID string) int {
	statsMutex.RLock()
	stats, exists := peerConnStats[peerID]
	statsMutex.RUnlock()

	if !exists {
		// No history - use default buffer size
		return baseBufferSize
	}

	stats.mutex.RLock()
	defer stats.mutex.RUnlock()

	// If we don't have enough history or it's too old, use default
	if len(stats.SpeedHistory) < 2 || time.Since(stats.LastUpdated) > 5*time.Minute {
		return baseBufferSize
	}

	// Calculate average speed from history (weighted toward recent samples)
	var weightedSum, weightSum float64
	for i, speed := range stats.SpeedHistory {
		weight := float64(i + 1) // Weight increases with recency
		weightedSum += speed * weight
		weightSum += weight
	}
	avgSpeed := weightedSum / weightSum

	// Calculate base buffer size from speed and BDP (Bandwidth-Delay Product)
	// Use RTT if available, otherwise estimate based on speed
	var rtt time.Duration
	if stats.RTT > 0 {
		rtt = stats.RTT
	} else {
		// Estimate RTT based on speed (higher speed usually means lower RTT)
		// This is a crude estimation - real RTT measurements would be better
		if avgSpeed > 50 {
			rtt = 20 * time.Millisecond // Fast connection
		} else if avgSpeed > 10 {
			rtt = 50 * time.Millisecond // Medium connection
		} else {
			rtt = 100 * time.Millisecond // Slow connection
		}
	}

	// BDP = bandwidth * RTT
	// Convert MB/s to bytes/sec and multiply by RTT
	bdpBytes := avgSpeed * 1024 * 1024 * rtt.Seconds()

	// Scale based on variance (more variance = larger buffer)
	varianceFactor := 1.0 + math.Sqrt(stats.ThroughputVar)/avgSpeed

	// Calculate new buffer size with limits
	newSize := int(bdpBytes * varianceFactor)

	// Apply limits
	if newSize < minBufferSize {
		newSize = minBufferSize
	} else if newSize > maxBufferSize {
		newSize = maxBufferSize
	}

	return newSize
}

// adaptBufferSize adjusts the buffer size based on current conditions
func adaptBufferSize(peerID string, currentBuffer int, currentSpeed float64, trend float64) int {
	// Get the optimal size as a target
	optimalSize := getOptimalBufferSize(peerID)

	// Start with optimal size as the target
	targetSize := optimalSize

	// Adjust based on trend (positive trend = improving speed)
	if trend > 0.1 {
		// Speed is improving - be more aggressive toward optimal
		targetSize = int(float64(optimalSize) * (1.0 + trend/2))
	} else if trend < -0.1 {
		// Speed is decreasing - be more conservative
		targetSize = int(float64(optimalSize) * (1.0 + trend/2))
	}

	// Calculate how much to adjust (more aggressive with higher adjustmentFactor)
	adjustment := float64(targetSize-currentBuffer) * adjustmentFactor

	// Apply the adjustment
	newSize := currentBuffer + int(adjustment)

	// Ensure we're within limits
	if newSize < minBufferSize {
		newSize = minBufferSize
	} else if newSize > maxBufferSize {
		newSize = maxBufferSize
	}

	return newSize
}

// calculateSpeedTrend determines if speed is increasing or decreasing
func calculateSpeedTrend(samples []float64) float64 {
	if len(samples) < 2 {
		return 0
	}

	// Use linear regression to find trend
	n := len(samples)
	sumX := 0.0
	sumY := 0.0
	sumXY := 0.0
	sumXX := 0.0

	for i, y := range samples {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}

	// Calculate slope
	slope := (float64(n)*sumXY - sumX*sumY) / (float64(n)*sumXX - sumX*sumX)

	// Normalize by average speed to get relative trend
	avgSpeed := sumY / float64(n)
	if avgSpeed > 0 {
		return slope / avgSpeed
	}
	return 0
}

// HandleFileStream processes incoming files with adaptive buffer sizing
// If outputPath is empty, defaults to "received_<peerID>_<timestamp>"
func HandleFileStream(s network.Stream, outputPath string) {
	defer s.Close()
	startTime := time.Now().UTC()
	peerID := s.Conn().RemotePeer().String()

	// Read file metadata (size and filename)
	header := make([]byte, 16+1024) // 16 bytes for size + 1024 for filename
	if _, err := io.ReadFull(s, header[:16]); err != nil {
		fmt.Println("Error reading size header:", err)
		return
	}

	fileSize := binary.LittleEndian.Uint64(header[:8])
	filenameLen := binary.LittleEndian.Uint16(header[8:10])

	// Read the filename if present
	var filename string
	if filenameLen > 0 {
		if _, err := io.ReadFull(s, header[16:16+filenameLen]); err != nil {
			fmt.Println("Error reading filename:", err)
			return
		}
		filename = string(header[16 : 16+filenameLen])
	}

	// Determine output path
	if outputPath == "" {
		if filename != "" {
			outputPath = filename
		} else {
			outputPath = fmt.Sprintf("received_%s_%d", peerID, time.Now().UTC().Unix())
		}
	} else {
		// If outputPath is a directory, append the original filename
		if fi, err := os.Stat(outputPath); err == nil && fi.IsDir() && filename != "" {
			outputPath = filepath.Join(outputPath, filepath.Base(filename))
		}
	}

	// Create parent directories if they don't exist
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		fmt.Println("Error creating directories:", err)
		return
	}

	// Get initial buffer size
	initialBuffer := getOptimalBufferSize(peerID)
	fmt.Printf("Receiving file '%s' from %s using initial buffer size: %d KB\n",
		filepath.Base(outputPath), peerID, initialBuffer/1024)

	// Create output file
	file, err := os.Create(outputPath)
	if err != nil {
		fmt.Println("Error creating file:", err)
		return
	}
	defer file.Close()

	// Use buffered reader with initial buffer size
	bufferSize := initialBuffer

	// Track performance for adaptation
	var bytesRead int64
	var speedSamples []float64
	var bufferSizes []int
	checkTime := time.Now().UTC()
	lastBytes := int64(0)

	// Create initial buffer
	buffer := make([]byte, bufferSize)

	// Create a reader that respects our buffer size
	reader := bufio.NewReader(s)

	for bytesRead < int64(fileSize) {
		// Check if we should measure speed and adapt
		if time.Since(checkTime) >= measureInterval {
			elapsed := time.Since(checkTime).Seconds()
			bytesInterval := bytesRead - lastBytes
			currentSpeed := float64(bytesInterval) / 1024 / 1024 / elapsed

			// Add to samples
			speedSamples = append(speedSamples, currentSpeed)
			bufferSizes = append(bufferSizes, bufferSize)

			// Keep window limited
			if len(speedSamples) > speedSampleWindow {
				speedSamples = speedSamples[1:]
				bufferSizes = bufferSizes[1:]
			}

			// Calculate trend and adapt buffer if we have enough data
			if len(speedSamples) >= 2 {
				trend := calculateSpeedTrend(speedSamples)
				oldBufferSize := bufferSize

				// Adapt buffer size based on algorithm
				bufferSize = adaptBufferSize(peerID, bufferSize, currentSpeed, trend)

				// If buffer size changed significantly, create new buffer
				if math.Abs(float64(bufferSize-oldBufferSize)) > float64(oldBufferSize)/5 {
					// Create a new buffer with the new size
					buffer = make([]byte, bufferSize)
					fmt.Printf("Adjusted buffer size: %d KB (speed: %.2f MB/s, trend: %.2f)\n",
						bufferSize/1024, currentSpeed, trend)
				}
			}

			// Update for next interval
			checkTime = time.Now().UTC()
			lastBytes = bytesRead
		}

		// SAFER APPROACH: Read only up to buffer capacity
		// This is the key fix - we limit our read to the actual buffer size
		toRead := bufferSize
		if remainingBytes := int64(fileSize) - bytesRead; remainingBytes < int64(toRead) {
			toRead = int(remainingBytes)
		}

		// Make sure our slice access is within bounds
		if toRead > len(buffer) {
			toRead = len(buffer)
		}

		// Read safely within buffer limits
		n, err := io.ReadFull(reader, buffer[:toRead])

		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			fmt.Println("Error receiving file:", err)
			return
		}

		if n > 0 {
			_, writeErr := file.Write(buffer[:n])
			if writeErr != nil {
				fmt.Println("Error writing to file:", writeErr)
				return
			}
			bytesRead += int64(n)
		}

		if bytesRead >= int64(fileSize) || err == io.EOF {
			break
		}
	}

	// Calculate final stats
	elapsedTime := time.Since(startTime).Seconds()
	avgSpeed := float64(bytesRead) / 1024 / 1024 / elapsedTime

	// Update connection stats
	addSpeedSample(peerID, avgSpeed, bufferSize)

	// Record metrics
	metrics.FileTransferDuration.WithLabelValues("received", peerID).Observe(elapsedTime)
	metrics.FileTransferSpeedMBPS.WithLabelValues("received", peerID).Observe(avgSpeed)

	fmt.Printf("Received file '%s' (%d bytes) from %s saved as %s (%.2f MB/s, final buffer: %d KB)\n",
		filename, fileSize, peerID, filepath.Base(outputPath), avgSpeed, bufferSize/1024)
}

// SendFile sends a file to a peer with adaptive buffer sizing
// If remotePath is empty, the base filename will be used
func SendFile(h host.Host, peerID peer.ID, filePath, remotePath string) error {
	// Start timing
	startTime := time.Now().UTC()
	peerIDStr := peerID.String()

	// Get file info
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("file stat failed: %v", err)
	}
	fileSize := fileInfo.Size()

	// Determine remote filename
	filename := filepath.Base(filePath)
	if remotePath != "" {
		filename = remotePath
	}

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("file open failed: %v", err)
	}
	defer file.Close()

	// Get initial buffer size
	initialBuffer := getOptimalBufferSize(peerIDStr)
	fmt.Printf("Sending file '%s' to %s as '%s' using initial buffer size: %d KB\n",
		filePath, peerIDStr, filename, initialBuffer/1024)

	// Open a stream with FileProtocol (QUIC)
	s, err := h.NewStream(context.Background(), peerID, config.FileProtocol)
	if err != nil {
		return fmt.Errorf("stream failed: %v", err)
	}
	defer s.Close()

	// Send file metadata (size and filename)
	header := make([]byte, 16+1024) // 16 bytes for size + 1024 for filename
	binary.LittleEndian.PutUint64(header[0:8], uint64(fileSize))

	// Encode filename length and content
	filenameBytes := []byte(filename)
	if len(filenameBytes) > 1024 {
		filenameBytes = filenameBytes[:1024] // Truncate if too long
	}
	binary.LittleEndian.PutUint16(header[8:10], uint16(len(filenameBytes)))
	copy(header[16:], filenameBytes)

	if _, err := s.Write(header[:16+len(filenameBytes)]); err != nil {
		return fmt.Errorf("header write failed: %v", err)
	}

	// Use buffered writer with initial size
	bufferSize := initialBuffer
	bufWriter := bufio.NewWriterSize(s, bufferSize)
	buffer := make([]byte, bufferSize)

	// Track performance for adaptation
	var bytesSent int64
	var speedSamples []float64
	var bufferSizes []int
	checkTime := time.Now().UTC()
	lastBytes := int64(0)

	for bytesSent < fileSize {
		// Check if we should measure speed and adapt
		if time.Since(checkTime) >= measureInterval && lastBytes > 0 {
			elapsed := time.Since(checkTime).Seconds()
			bytesInterval := bytesSent - lastBytes
			currentSpeed := float64(bytesInterval) / 1024 / 1024 / elapsed

			// Add to samples
			speedSamples = append(speedSamples, currentSpeed)
			bufferSizes = append(bufferSizes, bufferSize)

			// Keep window limited
			if len(speedSamples) > speedSampleWindow {
				speedSamples = speedSamples[1:]
				bufferSizes = bufferSizes[1:]
			}

			// Calculate trend and adapt buffer if we have enough data
			if len(speedSamples) >= 2 {
				trend := calculateSpeedTrend(speedSamples)
				oldBufferSize := bufferSize

				// Adapt buffer size based on algorithm
				bufferSize = adaptBufferSize(peerIDStr, bufferSize, currentSpeed, trend)

				// If buffer size changed significantly, create new writer
				if math.Abs(float64(bufferSize-oldBufferSize)) > float64(oldBufferSize)/5 {
					// Must flush before changing buffer size
					if err := bufWriter.Flush(); err != nil {
						return fmt.Errorf("flush failed: %v", err)
					}

					bufWriter = bufio.NewWriterSize(s, bufferSize)
					buffer = make([]byte, bufferSize)
					fmt.Printf("Adjusted buffer size: %d KB (speed: %.2f MB/s, trend: %.2f)\n",
						bufferSize/1024, currentSpeed, trend)
				}
			}

			// Update for next interval
			checkTime = time.Now().UTC()
			lastBytes = bytesSent
		}

		// Read file chunk with current buffer size
		n, err := file.Read(buffer[:bufferSize])
		if err != nil && err != io.EOF {
			return fmt.Errorf("file read failed: %v", err)
		}

		if n > 0 {
			// Write chunk to stream
			written, err := bufWriter.Write(buffer[:n])
			if err != nil {
				return fmt.Errorf("stream write failed: %v", err)
			}

			bytesSent += int64(written)

			// Flush periodically
			if bytesSent%int64(bufferSize*4) < int64(bufferSize) {
				if err := bufWriter.Flush(); err != nil {
					return fmt.Errorf("periodic flush failed: %v", err)
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	// Final flush
	if err := bufWriter.Flush(); err != nil {
		return fmt.Errorf("final flush failed: %v", err)
	}

	// Calculate final stats
	elapsedTime := time.Since(startTime).Seconds()
	avgSpeed := float64(bytesSent) / 1024 / 1024 / elapsedTime

	// Update connection stats
	addSpeedSample(peerIDStr, avgSpeed, bufferSize)

	// Record metrics
	metrics.FileTransferDuration.WithLabelValues("sent", peerIDStr).Observe(elapsedTime)
	metrics.FileTransferSpeedMBPS.WithLabelValues("sent", peerIDStr).Observe(avgSpeed)

	fmt.Printf("Sent file %s (%d bytes) to %s as '%s' (%.2f MB/s, final buffer: %d KB)\n",
		filePath, bytesSent, peerIDStr, filename, avgSpeed, bufferSize/1024)
	return nil
}
