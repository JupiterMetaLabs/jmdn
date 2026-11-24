package gatekeeper

import (
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"testing"
	"time"

	"gossipnode/config/settings"

	"github.com/golang-jwt/jwt/v5"
)

const audTestSecret = "test-jwt-secret-0123456789abcdef"

func signTestJWT(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(audTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func audCfg() *settings.SecurityConfig {
	return &settings.SecurityConfig{JWTSecret: audTestSecret}
}

func TestJWT_AudienceScopesService(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	tok := signTestJWT(t, jwt.MapClaims{"aud": settings.ServiceExplorerAPI, "exp": exp})
	policy := settings.Policy{AuthType: settings.AuthTypeToken, TokenEnv: "SEQUENCER_TOKEN"}

	if _, err := ValidateAuthHeader("Bearer "+tok, settings.ServiceExplorerAPI, policy, audCfg()); err != nil {
		t.Fatalf("explorer token rejected by explorer: %v", err)
	}
	if _, err := ValidateAuthHeader("Bearer "+tok, settings.ServiceBlockIngestHTTP, policy, audCfg()); err == nil {
		t.Fatal("explorer token must not open block ingest")
	}
}

func TestJWT_LegacyTypeClaim(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	tok := signTestJWT(t, jwt.MapClaims{"type": "explorer_api", "exp": exp})
	policy := settings.Policy{AuthType: settings.AuthTypeToken}
	if _, err := ValidateAuthHeader("Bearer "+tok, settings.ServiceExplorerAPI, policy, audCfg()); err != nil {
		t.Fatalf("legacy explorer token rejected: %v", err)
	}
	if _, err := ValidateAuthHeader("Bearer "+tok, settings.ServiceDID, policy, audCfg()); err == nil {
		t.Fatal("legacy explorer token must not open another service")
	}
}

func TestJWT_NoAudienceNoType_Rejected(t *testing.T) {
	tok := signTestJWT(t, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()})
	policy := settings.Policy{AuthType: settings.AuthTypeToken}
	if _, err := ValidateAuthHeader("Bearer "+tok, settings.ServiceExplorerAPI, policy, audCfg()); err == nil {
		t.Fatal("unscoped token must be rejected")
	}
}

func TestJWT_ExpRequired(t *testing.T) {
	tok := signTestJWT(t, jwt.MapClaims{"aud": settings.ServiceExplorerAPI})
	policy := settings.Policy{AuthType: settings.AuthTypeToken}
	if _, err := ValidateAuthHeader("Bearer "+tok, settings.ServiceExplorerAPI, policy, audCfg()); err == nil {
		t.Fatal("token without exp must be rejected")
	}
}

func TestHTTP_MTLSPolicyFailsClosedWithoutCert(t *testing.T) {
	m := &NetHTTPMiddleware{}
	policy := settings.Policy{AuthType: settings.AuthTypeMTLS}
	r := httptest.NewRequest("GET", "/", nil)
	if err := m.authenticate(r, "svc", policy); err == nil {
		t.Fatal("mtls policy over plain HTTP must not authenticate")
	}
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	if err := m.authenticate(r, "svc", policy); err != nil {
		t.Fatalf("verified client cert rejected: %v", err)
	}
}
