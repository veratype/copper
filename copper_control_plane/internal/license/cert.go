package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const certPrefix = "copper1"

// CertPayload is the signed claim bound to one machine fingerprint.
type CertPayload struct {
	V          int      `json:"v"`
	FP         string   `json:"fp"`
	Features   []string `json:"features"`
	IssuedAt   string   `json:"issued_at"`   // RFC3339 UTC
	ValidUntil string   `json:"valid_until"` // RFC3339 UTC
}

// Issue signs a certificate for fp valid until validUntil (UTC day/time).
// privateKey is a raw 64-byte ed25519 private key (seed+public) or 32-byte seed.
func Issue(privateKey ed25519.PrivateKey, fp string, features []string, validUntil time.Time) (string, error) {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		return "", fmt.Errorf("fingerprint is required")
	}
	if validUntil.IsZero() {
		return "", fmt.Errorf("valid_until is required")
	}
	if len(features) == 0 {
		features = []string{FeatureHWRAID, FeatureStorcli, FeatureSsacli}
	}
	priv, err := normalizePrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	payload := CertPayload{
		V:          1,
		FP:         fp,
		Features:   append([]string(nil), features...),
		IssuedAt:   time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		ValidUntil: validUntil.UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, body)
	return certPrefix + "." +
		base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseCert verifies signature with publicKey and returns the payload.
// It does not check fingerprint match or expiry — call ValidFor for that.
func ParseCert(cert string, publicKey ed25519.PublicKey) (CertPayload, error) {
	var zero CertPayload
	cert = strings.TrimSpace(cert)
	parts := strings.Split(cert, ".")
	if len(parts) != 3 || parts[0] != certPrefix {
		return zero, fmt.Errorf("invalid license format")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return zero, fmt.Errorf("invalid license payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return zero, fmt.Errorf("invalid license signature")
	}
	pub, err := normalizePublicKey(publicKey)
	if err != nil {
		return zero, err
	}
	if !ed25519.Verify(pub, body, sig) {
		return zero, fmt.Errorf("invalid license signature")
	}
	var p CertPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return zero, fmt.Errorf("invalid license payload")
	}
	if p.V != 1 || strings.TrimSpace(p.FP) == "" || strings.TrimSpace(p.ValidUntil) == "" {
		return zero, fmt.Errorf("invalid license payload")
	}
	return p, nil
}

// ValidFor reports whether payload unlocks feature for localFP at now.
func (p CertPayload) ValidFor(feature, localFP string, now time.Time) bool {
	if strings.TrimSpace(p.FP) != strings.TrimSpace(localFP) {
		return false
	}
	until, err := time.Parse(time.RFC3339, p.ValidUntil)
	if err != nil {
		return false
	}
	if now.UTC().After(until.UTC()) {
		return false
	}
	want := strings.TrimSpace(feature)
	for _, f := range p.Features {
		f = strings.TrimSpace(f)
		if f == want {
			return true
		}
		// hwraid grants both vendor backends.
		if f == FeatureHWRAID && (want == FeatureHWRAID || want == FeatureStorcli || want == FeatureSsacli) {
			return true
		}
	}
	return false
}

func normalizePrivateKey(k ed25519.PrivateKey) (ed25519.PrivateKey, error) {
	switch len(k) {
	case ed25519.PrivateKeySize:
		return k, nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(k), nil
	default:
		return nil, fmt.Errorf("ed25519 private key must be %d or %d bytes", ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}

func normalizePublicKey(k ed25519.PublicKey) (ed25519.PublicKey, error) {
	if len(k) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 public key must be %d bytes", ed25519.PublicKeySize)
	}
	return k, nil
}
