package ws

import (
	"context"
	"os/exec"
	"regexp"
)

var portRangePattern = regexp.MustCompile(`^\d{1,5}-\d{1,5}$`)

// CheckMosh verifies the Host prerequisite from FR-050: `mosh-server` is
// installed and the configured UDP port range is at least well-formed.
// It does NOT attempt to verify the range is reachable from the public
// internet — that can only be confirmed by a real client connecting
// through it, which is what contracts/mosh-terminal.md's handshake does.
func CheckMosh(ctx context.Context, udpPortRange string) ChecklistItem {
	const name = "mosh"

	if _, err := exec.LookPath("mosh-server"); err != nil {
		return ChecklistItem{
			Name:   name,
			Status: StatusPending,
			Error:  errorPtr(NewRequirementError(RequirementMosh, "mosh-server-not-found", "mosh-server no está instalado en el Host")),
		}
	}

	if !portRangePattern.MatchString(udpPortRange) {
		return ChecklistItem{
			Name:   name,
			Status: StatusPending,
			Error:  errorPtr(NewRequirementError(RequirementMosh, "mosh-port-range-invalid", "el rango de puertos UDP configurado no tiene el formato \"N-M\"")),
		}
	}

	return ChecklistItem{Name: name, Status: StatusVerified}
}

func errorPtr(e ErrorPayload) *ErrorPayload { return &e }
