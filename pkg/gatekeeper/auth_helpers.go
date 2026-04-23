package gatekeeper

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Sentinel errors for auth decisions — typed so callers can inspect.
var (
	errAuthRequired  = errors.New("authorization token required")
	errInvalidFormat = errors.New("invalid token format: expected 'Bearer <token>'")
	errInvalidToken  = errors.New("invalid token")
)

// parseBearer splits a raw Authorization header into scheme + token.
// Returns ("Bearer", "<token>", true) on success.
func parseBearer(header string) (scheme, token string, ok bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// validateJWT performs HMAC-SHA JWT validation against the given secret.
// Returns the parsed claims on success so callers don't need to re-parse.
func validateJWT(tokenString string, secret string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errInvalidToken
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errInvalidToken
	}
	return claims, nil
}
