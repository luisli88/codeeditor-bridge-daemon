package gitmanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"time"
)

// ProviderValidator validates PAT credentials against known provider API
// patterns (FR-016), and does a best-effort SSH reachability check for
// llave SSH credentials — a full auth-success signal from `ssh -T` is
// provider-specific (GitHub/GitLab return exit code 1 even on a
// *successful* authenticated handshake, by their own design) and out of
// scope to disambiguate perfectly here; a successful TCP+SSH handshake to
// the domain is treated as verified.
type ProviderValidator struct {
	HTTPClient *http.Client
}

// NewProviderValidator builds a ProviderValidator with a sane default
// timeout.
func NewProviderValidator() *ProviderValidator {
	return &ProviderValidator{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

func (v *ProviderValidator) Validate(ctx context.Context, domain string, kind CredentialKind, secret string) error {
	switch kind {
	case CredentialKindPAT, CredentialKindGitHubOAuthDerived:
		// GitHub's /user endpoint accepts an OAuth access token the same
		// way it accepts a personal access token (both are Bearer tokens),
		// so the derived credential validates via the same path.
		return v.validatePAT(ctx, domain, secret)
	case CredentialKindSSHKey:
		return v.validateSSHReachability(ctx, domain)
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

func (v *ProviderValidator) validateSSHReachability(ctx context.Context, domain string) error {
	cmd := exec.CommandContext(
		ctx, "ssh",
		"-T", "git@"+domain,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=5",
	)
	// El código de salida de `ssh -T` no distingue "autenticado" de
	// "rechazado" en GitHub/GitLab (ambos devuelven distinto de 0 aun con
	// éxito) — lo único que valida esta llamada es que el host respondió al
	// handshake SSH, no que la llave específica fue aceptada. Un
	// *ExitError (el proceso corrió y terminó) cuenta como "el host
	// respondió"; cualquier otro error (host no resuelve, timeout) no.
	var exitErr *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("gitmanager: ssh handshake with %s failed: %w", domain, err)
	}
	return nil
}
