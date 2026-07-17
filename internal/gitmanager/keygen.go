package gitmanager

import (
	"crypto/ed25519"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// marshalPrivateKey PEM-encodes an ed25519 private key in the OpenSSH
// format ("-----BEGIN OPENSSH PRIVATE KEY-----"). Go's own
// ssh.ParsePrivateKey (used by ImportSSHKey) is lenient enough to also
// accept a generic PKCS#8 PEM block, which this used to produce
// (x509.MarshalPKCS8PrivateKey) — but the real `ssh`/`git` binaries that
// actually clone/push/pull (GIT_SSH_COMMAND, see clone.go's
// credentialEnv) reject a PKCS#8-encoded ed25519 key outright with "Load
// key ...: invalid format": OpenSSH has never supported generic PKCS#8 for
// ed25519, only its own openssh-key-v1 wire format. Confirmed live against
// this package's real clone path, not just reasoned through.
func marshalPrivateKey(key ed25519.PrivateKey) (string, error) {
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return "", fmt.Errorf("gitmanager: marshal private key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), nil
}
