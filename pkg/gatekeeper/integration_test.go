package gatekeeper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gossipnode/config/settings"
	log "gossipnode/logging" // Import the app's logging package

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Mock Service Descriptor
var _PingService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "test.PingService",
	HandlerType: (*interface{})(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Ping",
			Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				return &emptypb.Empty{}, nil
			},
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "test.proto",
}

// setupTestEnv prepares a temp directory with CA and Service Certs.
func setupTestEnv(t *testing.T, serviceName string) (*settings.SecurityConfig, tls.Certificate, *x509.CertPool) {
	t.Helper()

	// 0. Initialize Settings (Required for Logger)
	_, err := settings.Load()
	require.NoError(t, err)

	// 1. Create Temp Dir
	tempDir := t.TempDir()

	// 2. Generate CA
	caCert, caKey, caPEM := createTestCA(t)
	err = os.WriteFile(filepath.Join(tempDir, "ca.crt"), caPEM, 0644)
	require.NoError(t, err)

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caPEM)

	// 3. Generate Server Cert
	// We ignore the returned cert struct, just use PEMs
	_, srvCertPEM, srvKeyPEM := issueTestCert(t, caCert, caKey, serviceName)
	err = os.WriteFile(filepath.Join(tempDir, serviceName+".crt"), srvCertPEM, 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tempDir, serviceName+".key"), srvKeyPEM, 0600)
	require.NoError(t, err)

	// 4. Generate Client Cert (for mTLS tests)
	clientCert, _, _ := issueTestCert(t, caCert, caKey, "test-admin-client")

	// 5. Config
	cfg := &settings.SecurityConfig{
		Enabled:     true,
		CertDir:     tempDir,
		IPCacheSize: 100,
		Services:    make(map[string]settings.Policy),
	}

	return cfg, clientCert, caPool
}

func TestGRPC_mTLS_Enforcement(t *testing.T) {
	serviceName := "grpc_mtls_test"
	secCfg, clientCert, caPool := setupTestEnv(t, serviceName)

	// Configure Policy: Strict mTLS
	secCfg.Services[serviceName] = settings.Policy{
		TLS:       true,
		AuthType:  settings.AuthTypeMTLS,
		RateLimit: 100,
		Burst:     100,
	}

	// Start Server
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()

	// Use App Logger
	appLog := log.NewAsyncLogger()
	// Ensure global is set (setup side effect)
	logger := appLog.Get().GlobalLogger

	srv, _, err := NewSecureGRPCServer(serviceName, secCfg, logger, false)
	require.NoError(t, err)

	// Register dummy service (mock struct can be empty since handler doesn't use it)
	srv.RegisterService(&_PingService_serviceDesc, &struct{}{})

	go srv.Serve(lis)
	defer srv.Stop()

	// Wait for start
	time.Sleep(50 * time.Millisecond)

	// Case 1: No TLS (Plaintext) -> Should Fail
	t.Run("Reject_Plaintext", func(t *testing.T) {
		conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		defer conn.Close()

		// Try to call
		var reply string
		err = conn.Invoke(context.Background(), "/test.PingService/Ping", nil, &reply)
		require.Error(t, err)
	})

	// Case 2: TLS but No Cert -> Should Fail (because AuthTypeMTLS)
	t.Run("Reject_NoClientCert", func(t *testing.T) {
		creds := credentials.NewTLS(&tls.Config{
			RootCAs: caPool, // Trust the server
			// No Certificates field -> No client cert
		})
		conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds))
		require.NoError(t, err)
		defer conn.Close()

		var reply string
		err = conn.Invoke(context.Background(), "/test.PingService/Ping", nil, &reply)
		require.Error(t, err)
		s, _ := status.FromError(err)

		// Expect Unauthenticated (16) or Unavailable (14) depending on failure stage
		validCodes := []codes.Code{codes.Unavailable, codes.Unauthenticated, codes.Unknown}
		assert.Contains(t, validCodes, s.Code(), "Expected rejection code, got %v", s.Code())
	})

	// Case 3: TLS with Valid Cert -> Should Pass
	t.Run("Accept_ValidClientCert", func(t *testing.T) {
		creds := credentials.NewTLS(&tls.Config{
			RootCAs:      caPool,
			Certificates: []tls.Certificate{clientCert},
		})
		conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds))
		require.NoError(t, err)
		defer conn.Close()

		reply := &emptypb.Empty{}
		err = conn.Invoke(context.Background(), "/test.PingService/Ping", nil, reply)
		require.NoError(t, err)
	})
}

func TestHTTP_RateLimiting(t *testing.T) {
	serviceName := "http_ratelimit_test"
	secCfg, _, _ := setupTestEnv(t, serviceName)

	// Policy: 50 requests total (burst), strict limit
	// To test reliably, we set low limits.
	// RateLimit: 10 RPS, Burst: 5.
	secCfg.Services[serviceName] = settings.Policy{
		TLS:       false, // Make it simpler for this test
		AuthType:  settings.AuthTypeNone,
		RateLimit: 10,
		Burst:     5,
	}

	// Setup Handler
	router := gin.New()
	appLog := log.NewAsyncLogger()
	logger := appLog.Get().GlobalLogger

	dummySrv := &http.Server{
		Addr:    ":0",
		Handler: router,
	}

	_, middleware, err := ConfigureHTTPServer(dummySrv, serviceName, secCfg, logger)
	require.NoError(t, err)

	// Apply middleware (PASS ServiceName)
	router.Use(middleware.Middleware(serviceName))

	router.GET("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})

	// Test Server
	ts := httptest.NewServer(router)
	defer ts.Close()

	client := ts.Client()

	// 1. First 5 requests (Burst) should pass
	for i := 0; i < 5; i++ {
		resp, err := client.Get(ts.URL + "/ping")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode, "Request %d should pass", i)
		resp.Body.Close()
	}

	// 2. Spam 20 requests extremely fast -> Should hit 429
	rejected := false
	for i := 0; i < 20; i++ {
		resp, err := client.Get(ts.URL + "/ping")
		require.NoError(t, err)
		if resp.StatusCode == 429 {
			rejected = true
			resp.Body.Close()
			break
		}
		resp.Body.Close()
	}

	assert.True(t, rejected, "Should have received at least one 429 Too Many Requests")
}
