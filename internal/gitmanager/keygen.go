package gitmanager

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// marshalPrivateKey PEM-encodes an ed25519 private key as PKCS#8 — the
// format `ssh.ParsePrivateKey` (used by ImportSSHKey) and standard OpenSSH
// tooling both accept.
func marshalPrivateKey(key ed25519.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("gitmanager: marshal private key: %w", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), nil
}
