package directMSG

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net"
    "strings"
    "sync"
    "time"

    "github.com/rs/zerolog"
    "github.com/rs/zerolog/log"
)

const (
    // YggdrasilPort is the port used for direct Yggdrasil messaging
    YggdrasilPort = 15001
    
    // MaxMessageSize is the maximum size of a message in bytes (1MB)
    MaxMessageSize = 1024 * 1024
    
    // ConnectionTimeout for establishing connections
    ConnectionTimeout = 10 * time.Second
    
    // ReadTimeout for reading from connections
    ReadTimeout = 5 * time.Second
)

// MessageEvent represents a message event for logging and metrics
type MessageEvent struct {
    Type      string    `json:"type"`      // "sent" or "received"
    Source    string    `json:"source"`    // Source address
    Target    string    `json:"target"`    // Target address
    Timestamp time.Time `json:"timestamp"` // When the event occurred
    Success   bool      `json:"success"`   // Whether operation succeeded
    Error     string    `json:"error,omitempty"` // Error message if any
}

// Connection pool for reusing connections
var (
    connectionPool     = make(map[string]*pooledConnection)
    connectionPoolLock sync.Mutex
)

type pooledConnection struct {
    conn      net.Conn
    lastUsed  time.Time
    expiresAt time.Time
}

// Metrics for monitoring
var (
    messagesSent     int64
    messagesReceived int64
    messagesFailed   int64
    metricsLock      sync.Mutex
)

// StartYggdrasilListener starts a TCP listener for direct Yggdrasil messages
func StartYggdrasilListener(ctx context.Context) {
    // Listen on all interfaces (both regular and Yggdrasil)
    listener, err := net.Listen("tcp6", fmt.Sprintf(":%d", YggdrasilPort))
    if err != nil {
        log.Error().Err(err).Int("port", YggdrasilPort).Msg("Failed to start Yggdrasil listener")
        return
    }
    
    log.Info().Int("port", YggdrasilPort).Msg("Started Yggdrasil message listener")
    
    // Start the connection pool cleaner
    go cleanConnectionPool(ctx)
    
    go func() {
        defer func() {
            log.Info().Msg("Closing Yggdrasil listener")
            listener.Close()
        }()
        
        for {
            select {
            case <-ctx.Done():
                return
            default:
                // Set accept deadline to make context cancellation responsive
                if deadline, ok := ctx.Deadline(); ok {
                    listener.(*net.TCPListener).SetDeadline(deadline)
                } else {
                    listener.(*net.TCPListener).SetDeadline(time.Now().Add(1 * time.Second))
                }
                
                conn, err := listener.Accept()
                if err != nil {
                    // Check if the error is temporary
                    if ne, ok := err.(net.Error); ok && ne.Temporary() {
                        log.Debug().Err(err).Msg("Temporary error accepting Yggdrasil connection")
                        time.Sleep(100 * time.Millisecond)
                        continue
                    }
                    
                    // If context is done, this is expected
                    if ctx.Err() != nil {
                        return
                    }
                    
                    log.Error().Err(err).Msg("Error accepting Yggdrasil connection")
                    time.Sleep(1 * time.Second) // Brief pause before retrying
                    continue
                }
                
                // Handle each connection in a goroutine
                go handleYggdrasilConnection(conn)
            }
        }
    }()
}

// handleYggdrasilConnection processes incoming Yggdrasil messages
func handleYggdrasilConnection(conn net.Conn) {
    defer conn.Close()
    
    // Get remote address for display and logging
    remoteAddr := conn.RemoteAddr().String()
    log.Debug().Str("remote_addr", remoteAddr).Msg("New Yggdrasil connection")
    
    // Set reasonable read deadline
    conn.SetReadDeadline(time.Now().Add(ReadTimeout))
    
    // Use a limited reader to prevent memory exhaustion
    limitedReader := io.LimitReader(conn, MaxMessageSize)
    reader := bufio.NewReader(limitedReader)
    
    // Read the message
    message, err := reader.ReadString('\n')
    if err != nil {
        log.Error().Err(err).Str("remote_addr", remoteAddr).Msg("Error reading Yggdrasil message")
        logMessageEvent(MessageEvent{
            Type:      "received",
            Source:    remoteAddr,
            Timestamp: time.Now(),
            Success:   false,
            Error:     err.Error(),
        })
        return
    }
    
    // Update metrics
    incrementMessagesReceived()
    
    // Trim message
    message = strings.TrimSpace(message)
    
    // Check protocol signature
    prefix := ""
    if strings.HasPrefix(message, "YGGMSG:") {
        message = strings.TrimPrefix(message, "YGGMSG:")
        message = strings.TrimSpace(message)
        prefix = "YGGMSG"
    }
    
    // Log the receive event
    logMessageEvent(MessageEvent{
        Type:      "received",
        Source:    remoteAddr,
        Timestamp: time.Now(),
        Success:   true,
    })
    
    // Display the message with formatting
    fmt.Printf("\n\033[1;42m                               \033[0m\n")
    fmt.Printf("\033[1;42m  YGGDRASIL MESSAGE RECEIVED    \033[0m\n")
    if prefix != "" {
        fmt.Printf("\033[1;42m  Protocol: %-16s     \033[0m\n", prefix)
    }
    fmt.Printf("\033[1;42m                               \033[0m\n\n")
    fmt.Printf("From: \033[1;36m%s\033[0m\n", remoteAddr)
    fmt.Printf("Time: \033[1;33m%s\033[0m\n", time.Now().Format(time.RFC3339))
    fmt.Printf("\nContent:\n\033[1;37m%s\033[0m\n\n", message)
    
    // Send acknowledgment
    ack := "ACK\n"
    if prefix != "" {
        ack = prefix + ":" + ack
    }
    conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
    _, err = conn.Write([]byte(ack))
    if err != nil {
        log.Warn().Err(err).Str("remote_addr", remoteAddr).Msg("Failed to send acknowledgment")
    }
}

// SendViaYggdrasil sends a message directly using Yggdrasil IPv6 networking
func SendViaYggdrasil(target string, message string) error {
    // Extract the Yggdrasil IPv6 address from the target
    yggIP := extractYggdrasilIP(target)
    if yggIP == "" {
        err := fmt.Errorf("no Yggdrasil IPv6 address found in target: %s", target)
        logMessageEvent(MessageEvent{
            Type:      "sent",
            Target:    target,
            Timestamp: time.Now(),
            Success:   false,
            Error:     err.Error(),
        })
        return err
    }
    
    // Format target address
    targetAddr := fmt.Sprintf("[%s]:%d", yggIP, YggdrasilPort)
    
    log.Debug().Str("yggdrasil_ip", yggIP).Str("target_addr", targetAddr).Msg("Sending via Yggdrasil")
    fmt.Printf("Sending via Yggdrasil to IPv6 address: %s\n", yggIP)
    
    // Try to get a connection from the pool
    conn, err := getConnection(targetAddr)
    if err != nil {
        logMessageEvent(MessageEvent{
            Type:      "sent",
            Target:    targetAddr,
            Timestamp: time.Now(),
            Success:   false,
            Error:     err.Error(),
        })
        incrementMessagesFailed()
        return fmt.Errorf("Yggdrasil connection failed: %v", err)
    }
    
    // Format message with protocol signature and trailing newline
    formattedMessage := fmt.Sprintf("YGGMSG:%s\n", message)
    
    // Set write deadline
    conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
    
    // Send the message
    _, err = conn.Write([]byte(formattedMessage))
    if err != nil {
        // Connection failed, remove it from pool
        removeConnection(targetAddr)
        
        logMessageEvent(MessageEvent{
            Type:      "sent",
            Target:    targetAddr,
            Timestamp: time.Now(),
            Success:   false,
            Error:     err.Error(),
        })
        
        incrementMessagesFailed()
        return fmt.Errorf("failed to send message: %v", err)
    }
    
    // Wait for acknowledgment
    conn.SetReadDeadline(time.Now().Add(ReadTimeout))
    reader := bufio.NewReader(conn)
    response, err := reader.ReadString('\n')
    
    if err != nil {
        // Connection might be broken, remove it from pool
        removeConnection(targetAddr)
        
        logMessageEvent(MessageEvent{
            Type:      "sent",
            Target:    targetAddr,
            Timestamp: time.Now(),
            Success:   false,
            Error:     "no acknowledgment: " + err.Error(),
        })
        
        incrementMessagesFailed()
        return fmt.Errorf("no acknowledgment received: %v", err)
    }
    
    // Return connection to pool
    returnConnectionToPool(targetAddr, conn)
    
    // Update metrics
    incrementMessagesSent()
    
    // Log the event
    logMessageEvent(MessageEvent{
        Type:      "sent",
        Target:    targetAddr,
        Timestamp: time.Now(),
        Success:   true,
    })
    
    response = strings.TrimSpace(response)
    fmt.Printf("Message sent successfully via Yggdrasil to %s\n", yggIP)
    fmt.Printf("Received response: %s\n", response)
    return nil
}

// extractYggdrasilIP extracts Yggdrasil IPv6 address from a multiaddress string
func extractYggdrasilIP(addr string) string {
    // Method 1: Extract from multiaddr format
    parts := strings.Split(addr, "/")
    for i, part := range parts {
        if i > 0 && part == "ip6" && i+1 < len(parts) {
            ip := parts[i+1]
            if strings.HasPrefix(ip, "200:") || strings.HasPrefix(ip, "201:") {
                return ip
            }
        }
    }
    
    // Method 2: Check if the entire string is a Yggdrasil IPv6
    if strings.HasPrefix(addr, "200:") || strings.HasPrefix(addr, "201:") {
        // Remove brackets if present
        return strings.Trim(addr, "[]")
    }
    
    return ""
}

// SendYggdrasilMessage is the main function to call for sending a message
func SendYggdrasilMessage(targetAddr, message string) error {
    return SendViaYggdrasil(targetAddr, message)
}

// GetMetrics returns current messaging metrics
func GetMetrics() map[string]int64 {
    metricsLock.Lock()
    defer metricsLock.Unlock()
    
    return map[string]int64{
        "messages_sent":     messagesSent,
        "messages_received": messagesReceived,
        "messages_failed":   messagesFailed,
    }
}

// Connection pool management

func getConnection(targetAddr string) (net.Conn, error) {
    connectionPoolLock.Lock()
    defer connectionPoolLock.Unlock()
    
    // Check if we have a valid connection in the pool
    if pooled, exists := connectionPool[targetAddr]; exists {
        if time.Now().Before(pooled.expiresAt) {
            // Connection is still valid
            pooled.lastUsed = time.Now()
            return pooled.conn, nil
        }
        // Connection expired, close it
        pooled.conn.Close()
        delete(connectionPool, targetAddr)
    }
    
    // Create a new connection
    conn, err := net.DialTimeout("tcp6", targetAddr, ConnectionTimeout)
    if err != nil {
        return nil, err
    }
    
    return conn, nil
}

func returnConnectionToPool(targetAddr string, conn net.Conn) {
    connectionPoolLock.Lock()
    defer connectionPoolLock.Unlock()
    
    // Store connection in pool with expiration
    connectionPool[targetAddr] = &pooledConnection{
        conn:      conn,
        lastUsed:  time.Now(),
        expiresAt: time.Now().Add(30 * time.Second),
    }
}

func removeConnection(targetAddr string) {
    connectionPoolLock.Lock()
    defer connectionPoolLock.Unlock()
    
    if pooled, exists := connectionPool[targetAddr]; exists {
        pooled.conn.Close()
        delete(connectionPool, targetAddr)
    }
}

func cleanConnectionPool(ctx context.Context) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            connectionPoolLock.Lock()
            now := time.Now()
            
            for addr, conn := range connectionPool {
                if now.After(conn.expiresAt) {
                    conn.conn.Close()
                    delete(connectionPool, addr)
                }
            }
            
            connectionPoolLock.Unlock()
        }
    }
}

// Metrics helpers

func incrementMessagesSent() {
    metricsLock.Lock()
    defer metricsLock.Unlock()
    messagesSent++
}

func incrementMessagesReceived() {
    metricsLock.Lock()
    defer metricsLock.Unlock()
    messagesReceived++
}

func incrementMessagesFailed() {
    metricsLock.Lock()
    defer metricsLock.Unlock()
    messagesFailed++
}

// logMessageEvent logs a message event
func logMessageEvent(event MessageEvent) {
    // Log to structured logger
    logEvent := log.Info()
    if !event.Success {
        logEvent = log.Error()
    }
    
    logEvent.
        Str("event_type", "yggdrasil_message").
        Str("operation", event.Type).
        Time("timestamp", event.Timestamp)
    
    if event.Source != "" {
        logEvent.Str("source", event.Source)
    }
    
    if event.Target != "" {
        logEvent.Str("target", event.Target)
    }
    
    if event.Error != "" {
        logEvent.Str("error", event.Error)
    }
    
    logEvent.Bool("success", event.Success).Msg("Yggdrasil message event")
    
    // Also log as JSON for potential post-processing
    if jsonBytes, err := json.Marshal(event); err == nil {
        log.Debug().RawJSON("event_json", jsonBytes).Msg("Message event JSON")
    }
}

func init() {
    // Initialize the logger
    log.Logger = log.Output(zerolog.ConsoleWriter{Out: io.Discard})
    
    // This would typically be in your main application to enable console output
    // log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
}