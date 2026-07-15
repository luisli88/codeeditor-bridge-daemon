package gitmanager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/crypto/ssh"
)

// ProviderValidator validates PAT credentials against known provider API
// patterns (FR-016), and a llave SSH by actually authenticating with it
// against the domain's SSH server as the `git` user — the same identity
// `git@github.com` clone/push URLs use, and the only one GitHub (and
// GitLab/Bitbucket) accept public-key auth for. A successful
// `ssh.Dial`/handshake means the provider accepted *this* key, not just
// that the host has an SSH server running.
type ProviderValidator struct {
	HTTPClient *http.Client
	// SSHPort overrides the default 22 — only ever set by tests, against a
	// local fake SSH server instead of a real provider.
	SSHPort string
}

// NewProviderValidator builds a ProviderValidator with a sane default
// timeout.
func NewProviderValidator() *ProviderValidator {
	return &ProviderValidator{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

func (v *ProviderValidator) sshPort() string {
	if v.SSHPort == "" {
		return "22"
	}
	return v.SSHPort
}

func (v *ProviderValidator) Validate(ctx context.Context, domain string, kind CredentialKind, secret string) error {
	switch kind {
	case CredentialKindPAT, CredentialKindGitHubOAuthDerived:
		// GitHub's /user endpoint accepts an OAuth access token the same
		// way it accepts a personal access token (both are Bearer tokens),
		// so the derived credential validates via the same path.
		return v.validatePAT(ctx, domain, secret)
	case CredentialKindSSHKey:
		return v.validateSSHKey(domain, secret)
	default:
		return fmt.Errorf("gitmanager: unknown credential kind %q", kind)
	}
}

// validatePAT calls the provider's "who am I" endpoint. GitHub and GitLab
// have well-known API shapes; any other domain is assumed to be a
// self-hosted GitLab-compatible instance (spec.md → Assumptions: solo
// GitHub tiene integración nativa, el resto es PAT + dominio genérico).
func (v *ProviderValidator) validatePAT(ctx context.Context, domain, token string) error {
	url, authHeader := patValidationRequest(domain, token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("gitmanager: build validation request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("User-Agent", "CodeEditor")

	resp, err := v.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gitmanager: validation request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gitmanager: %s rejected the token (HTTP %d)", domain, resp.StatusCode)
	}
	return nil
}

func patValidationRequest(domain, token string) (url string, authHeader string) {
	switch domain {
	case "github.com":
		return "https://api.github.com/user", "Bearer " + token
	case "bitbucket.org":
		return "https://api.bitbucket.org/2.0/user", "Bearer " + token
	default:
		// gitlab.com y cualquier instancia self-hosted GitLab-compatible.
		return fmt.Sprintf("https://%s/api/v4/user", domain), "Bearer " + token
	}
}

// validateSSHKey actually authenticates with privateKeyPEM against
// domain:22 as the `git` user — completing the SSH handshake *and*
// public-key auth is only possible if the provider has this exact key on
// file, unlike a bare TCP/handshake reachability check (which every
// GitHub/GitLab-style host passes regardless of which key, or whether any
// key at all, is being offered). Host key verification is intentionally
// not pinned (same trust-on-first-use trade-off as `SelfSignedTrust.swift`/
// `RemoteSetupService`'s `.acceptAnything()` on the app side) — there's no
// prior known-hosts state for a provider the Bridge Daemon has never
// talked to before, and the thing being verified here is the *client's*
// key, not the server's identity.
func (v *ProviderValidator) validateSSHKey(domain, privateKeyPEM string) error {
	signer, err := ssh.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return fmt.Errorf("gitmanager: parse SSH private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", net.JoinHostPort(domain, v.sshPort()), config)
	if err != nil {
		return fmt.Errorf("gitmanager: %s rejected the SSH key: %w", domain, err)
	}
	defer client.Close() //nolint:errcheck
	return nil
}
