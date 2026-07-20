// Package auth implements the PKCE loopback login flow.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// GenerateVerifier returns a 43-char base64url PKCE verifier from 32 random bytes.
// The randomness is from crypto/rand; a read failure is a hard error, never a
// fallback to a weaker source.
func GenerateVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand read failed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Challenge returns the unpadded base64url SHA-256 of the verifier string (the
// server validates the challenge against ^[A-Za-z0-9_-]{43}$).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState returns a base64url state nonce from 16 random bytes.
func GenerateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand read failed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
