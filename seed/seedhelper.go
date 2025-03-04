package seed

import (
    "context"
    "errors"
    "fmt"
    "strings"
    "time"
	"gossipnode/config"
	"database/sql"
	"github.com/libp2p/go-libp2p/core/host"
    "github.com/libp2p/go-libp2p/core/network"
    "github.com/libp2p/go-libp2p/core/peer"
    "github.com/multiformats/go-multiaddr"
)

type PeerStatus struct {
    PeerID      string `json:"peerId"`
    PublicAddr  string `json:"publicAddr"`
    Connections int    `json:"connections"`
    LastSeen    int64  `json:"lastSeen"`
    IsOnline    bool   `json:"isOnline"`
}

type SeedNode struct {
    db     *sql.DB
    host   host.Host
    peerID peer.ID
}

// Define HeartbeatProtocol if not already defined elsewhere
const (
)

// For functions that you reference without implementation:
func (sn *SeedNode) GetAllPeers() ([]string, error) {
    // Implementation if not already defined elsewhere
    rows, err := sn.db.Query("SELECT publicaddr FROM peers")
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var peers []string
    for rows.Next() {
        var addr string
        if err := rows.Scan(&addr); err != nil {
            return nil, err
        }
        peers = append(peers, addr)
    }
    
    return peers, nil
}

func (sn *SeedNode) GetPeers() ([]string, error) {
    // Implementation if not already defined elsewhere
    rows, err := sn.db.Query("SELECT publicaddr FROM peers WHERE connections < 6 LIMIT 4")
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var peers []string
    for rows.Next() {
        var addr string
        if err := rows.Scan(&addr); err != nil {
            return nil, err
        }
        peers = append(peers, addr)
    }
    
    return peers, nil
}

// ValidateMultiaddr checks if a multiaddress is valid
func ValidateMultiaddr(addr string) (string, error) {
    // Check if address format is correct
    if !strings.Contains(addr, "/p2p/") {
        return "", errors.New("invalid multiaddress format: missing /p2p/ component")
    }

    // Parse multiaddr
    maddr, err := multiaddr.NewMultiaddr(addr)
    if err != nil {
        return "", fmt.Errorf("invalid multiaddress: %w", err)
    }

    // Extract peer ID
    addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
    if err != nil {
        return "", fmt.Errorf("failed to extract peer info: %w", err)
    }

    // Return the peer ID
    return addrInfo.ID.String(), nil
}

// AddPeer adds a peer to the seed node's database
func (sn *SeedNode) AddPeer(peerAddr string) error {
    // Validate multiaddress and extract peer ID
    peerID, err := ValidateMultiaddr(peerAddr)
    if err != nil {
        return err
    }

    // Check if this peer already exists
    var exists bool
    err = sn.db.QueryRow("SELECT EXISTS(SELECT 1 FROM peers WHERE peerID = ?)", peerID).Scan(&exists)
    if err != nil {
        return fmt.Errorf("database error checking peer existence: %w", err)
    }

    if exists {
        // Update the peer's address
        _, err = sn.db.Exec("UPDATE peers SET publicaddr = ? WHERE peerID = ?", peerAddr, peerID)
        return err
    }

    // Insert new peer
    _, err = sn.db.Exec("INSERT INTO peers (publicaddr, peerID, connections) VALUES (?, ?, ?)", 
        peerAddr, peerID, 0)
    
    if err != nil {
        return fmt.Errorf("failed to insert peer into database: %w", err)
    }

    fmt.Printf("Added peer %s to database\n", peerID)
    return nil
}

// DeletePeer removes a peer from the seed node's database
func (sn *SeedNode) DeletePeer(peerID string) error {
    // If input is a full multiaddress, extract just the peer ID
    if strings.Contains(peerID, "/p2p/") {
        parts := strings.Split(peerID, "/p2p/")
        if len(parts) > 1 {
            peerID = parts[1]
        }
    }

    // Delete the peer from the database
    result, err := sn.db.Exec("DELETE FROM peers WHERE peerID = ?", peerID)
    if err != nil {
        return fmt.Errorf("database error deleting peer: %w", err)
    }

    // Check if any row was affected
    rows, err := result.RowsAffected()
    if err != nil {
        return fmt.Errorf("failed to get affected rows: %w", err)
    }

    if rows == 0 {
        return fmt.Errorf("peer %s not found in database", peerID)
    }

    fmt.Printf("Deleted peer %s from database\n", peerID)
    return nil
}

// PingPeer attempts to ping a peer and returns whether it's online
func (sn *SeedNode) PingPeer(peerAddr string) (bool, error) {
    var peerInfo peer.AddrInfo
    var err error

    // Check if it's a full multiaddress or just a peer ID
    if strings.Contains(peerAddr, "/") {
        // Parse as multiaddress
        addr, err := multiaddr.NewMultiaddr(peerAddr)
        if err != nil {
            return false, fmt.Errorf("invalid multiaddress: %w", err)
        }

        pInfo, err := peer.AddrInfoFromP2pAddr(addr)
        if err != nil {
            return false, fmt.Errorf("failed to extract peer info: %w", err)
        }
        peerInfo = *pInfo
    } else {
        // Parse as peer ID
        pid, err := peer.Decode(peerAddr)
        if err != nil {
            return false, fmt.Errorf("invalid peer ID: %w", err)
        }
        peerInfo = peer.AddrInfo{ID: pid}
    }

    // If we don't have addresses and it's just a peer ID, try to get from database
    if len(peerInfo.Addrs) == 0 {
        var publicAddr string
        err := sn.db.QueryRow("SELECT publicaddr FROM peers WHERE peerID = ?", peerInfo.ID.String()).Scan(&publicAddr)
        if err != nil {
            return false, fmt.Errorf("peer not found in database: %w", err)
        }

        // Parse the stored address
        addr, err := multiaddr.NewMultiaddr(publicAddr)
        if err != nil {
            return false, fmt.Errorf("invalid stored address: %w", err)
        }

		peerInfo.Addrs = []multiaddr.Multiaddr{addr}

        // Extract just the address part (without the peer ID)
        addrParts := strings.Split(publicAddr, "/p2p/")
        if len(addrParts) > 0 {
            addrOnly, err := multiaddr.NewMultiaddr(addrParts[0])
            if err == nil {
                peerInfo.Addrs = []multiaddr.Multiaddr{addrOnly}
            }
        }
    }

    // Set a timeout for the ping operation
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Try to connect to the peer
    if err = sn.host.Connect(ctx, peerInfo); err != nil {
        fmt.Printf("Failed to connect to peer %s: %v\n", peerInfo.ID, err)
        return false, nil // Not an error, just offline
    }

    // Try to open a heartbeat stream
    s, err := sn.host.NewStream(ctx, peerInfo.ID, config.HeartbeatProtocol)
    if err != nil {
        fmt.Printf("Failed to open heartbeat stream to %s: %v\n", peerInfo.ID, err)
        return false, nil // Not an error, just offline
    }
    defer s.Close()

    // Update last seen time in database
    _, err = sn.db.Exec("UPDATE peers SET connections = connections + 1 WHERE peerID = ?", peerInfo.ID.String())
    if err != nil {
        return true, fmt.Errorf("database error updating peer status: %w", err)
    }

    fmt.Printf("Successfully pinged peer %s\n", peerInfo.ID)
    return true, nil
}

// GetPeerStatus returns detailed status for a specific peer
func (sn *SeedNode) GetPeerStatus(peerID string) (*PeerStatus, error) {
    // If input is a full multiaddress, extract just the peer ID
    if strings.Contains(peerID, "/p2p/") {
        parts := strings.Split(peerID, "/p2p/")
        if len(parts) > 1 {
            peerID = parts[1]
        }
    }

    // Query the database for the peer
    var status PeerStatus
    err := sn.db.QueryRow("SELECT peerID, publicAddr, connections FROM peers WHERE peerID = ?", peerID).
        Scan(&status.PeerID, &status.PublicAddr, &status.Connections)

    if err != nil {
        return nil, fmt.Errorf("peer not found: %w", err)
    }

    // Check if the peer is currently online
    isOnline, _ := sn.PingPeer(status.PublicAddr)
    status.IsOnline = isOnline
    status.LastSeen = time.Now().Unix()

    return &status, nil
}

// ListPeers returns a list of all peers with their status
func (sn *SeedNode) ListPeers() ([]*PeerStatus, error) {
    rows, err := sn.db.Query("SELECT peerID, publicAddr, connections FROM peers")
    if err != nil {
        return nil, fmt.Errorf("database error listing peers: %w", err)
    }
    defer rows.Close()

    var peers []*PeerStatus
    for rows.Next() {
        var status PeerStatus
        if err := rows.Scan(&status.PeerID, &status.PublicAddr, &status.Connections); err != nil {
            return nil, fmt.Errorf("database error scanning peer: %w", err)
        }

        // Don't ping automatically as it can be slow for large lists
        status.IsOnline = false
        status.LastSeen = 0
        peers = append(peers, &status)
    }

    return peers, nil
}

// SetupPeerMaintenanceRoutine starts a goroutine that periodically checks peer status
func (sn *SeedNode) SetupPeerMaintenanceRoutine(checkInterval time.Duration) {
    go func() {
        ticker := time.NewTicker(checkInterval)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                sn.performPeerMaintenance()
            }
        }
    }()
    fmt.Printf("Peer maintenance routine started with interval %v\n", checkInterval)
}

// performPeerMaintenance checks all peers and removes unresponsive ones
func (sn *SeedNode) performPeerMaintenance() {
    fmt.Println("Performing peer maintenance...")
    
    // Get all peers
    peers, err := sn.GetAllPeers()
    if err != nil {
        fmt.Printf("Error getting peers: %v\n", err)
        return
    }

    // Check each peer
    for _, peerAddr := range peers {
        isOnline, err := sn.PingPeer(peerAddr)
        if err != nil {
            fmt.Printf("Error pinging peer %s: %v\n", peerAddr, err)
            continue
        }

        // If peer is not online, attempt to ping again a few times
        if !isOnline {
            var peerID string
            if strings.Contains(peerAddr, "/p2p/") {
                parts := strings.Split(peerAddr, "/p2p/")
                if len(parts) > 1 {
                    peerID = parts[1]
                }
            }

            // How many failed attempts before removing
            const maxAttempts = 3
            var attempts int
            
            err := sn.db.QueryRow("SELECT connections FROM peers WHERE peerID = ?", peerID).Scan(&attempts)
            if err != nil {
                fmt.Printf("Error retrieving peer %s attempts: %v\n", peerID, err)
                continue
            }

            if attempts >= maxAttempts {
                fmt.Printf("Peer %s has failed %d connect attempts, removing from database\n", peerID, attempts)
                
                // Remove the peer
                if err := sn.DeletePeer(peerID); err != nil {
                    fmt.Printf("Error removing peer %s: %v\n", peerID, err)
                }
            } else {
                // Increment the attempt counter
                _, err = sn.db.Exec("UPDATE peers SET connections = connections + 1 WHERE peerID = ?", peerID)
                if err != nil {
                    fmt.Printf("Error updating peer %s attempt counter: %v\n", peerID, err)
                }
            }
        } else {
            // Reset attempts for online peers
            if strings.Contains(peerAddr, "/p2p/") {
                parts := strings.Split(peerAddr, "/p2p/")
                if len(parts) > 1 {
                    peerID := parts[1]
                    _, err = sn.db.Exec("UPDATE peers SET connections = 0 WHERE peerID = ?", peerID)
                    if err != nil {
                        fmt.Printf("Error resetting peer %s attempt counter: %v\n", peerID, err)
                    }
                }
            }
        }
    }

    fmt.Println("Peer maintenance completed")
}

// RegisterPeer is called by a peer to register itself with the seed node
func (sn *SeedNode) RegisterPeer(stream network.Stream) {
    defer stream.Close()
    
    // Get the peer's info
    remotePeer := stream.Conn().RemotePeer()
    remoteAddr := stream.Conn().RemoteMultiaddr()
    
    // Create a full multiaddress for this peer
    fullAddr := fmt.Sprintf("%s/p2p/%s", remoteAddr.String(), remotePeer.String())
    
    // Add the peer to the database
    err := sn.AddPeer(fullAddr)
    if err != nil {
        fmt.Printf("Error registering peer %s: %v\n", remotePeer, err)
        return
    }
    
    // Send back acknowledgment with 4 random peers
    peers, err := sn.GetPeers()
    if err != nil {
        fmt.Printf("Error getting peers for %s: %v\n", remotePeer, err)
        return
    }
    
    // Format response
    response := "REGISTERED|"
    if len(peers) > 0 {
        response += strings.Join(peers, ",")
    }
    
    _, err = stream.Write([]byte(response + "\n"))
    if err != nil {
        fmt.Printf("Error sending registration response to %s: %v\n", remotePeer, err)
    }
}

// SetupStreamHandler sets up a handler for peer registration
func (sn *SeedNode) SetupStreamHandler() {
    // Register a custom protocol for peer registration
    const RegisterProtocol = "/seednode/register/1.0.0"
    
    sn.host.SetStreamHandler(RegisterProtocol, func(stream network.Stream) {
        sn.RegisterPeer(stream)
    })
    
    fmt.Println("Peer registration handler setup complete")
}