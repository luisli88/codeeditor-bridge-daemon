package session

import "testing"

// FR-047: a login prompt line (URL + verification code) signals
// auth-required exactly once.
func TestAuthDetector_ScanTwice_OnlyFirstPromptSignals(t *testing.T) {
	detector := NewAuthDetector()

	first := detector.scan("ws-1", []byte("Visit https://claude.ai/device and enter code ABCD-1234\n"))
	if first == nil || first.Type != "auth-required" {
		t.Fatalf("expected auth-required, got %+v", first)
	}
	if first.LoginURL != "https://claude.ai/device" {
		t.Errorf("unexpected LoginURL: %q", first.LoginURL)
	}
	if first.VerificationCode != "ABCD-1234" {
		t.Errorf("unexpected VerificationCode: %q", first.VerificationCode)
	}

	second := detector.scan("ws-1", []byte("Visit https://claude.ai/device and enter code ABCD-1234\n"))
	if second != nil {
		t.Errorf("must not signal auth-required twice while still waiting on the same prompt, got %+v", second)
	}
}

func TestAuthDetector_SuccessAfterPrompt_SignalsAuthComplete(t *testing.T) {
	detector := NewAuthDetector()
	_ = detector.scan("ws-1", []byte("Visit https://claude.ai/device and enter code ABCD-1234\n"))

	event := detector.scan("ws-1", []byte("Successfully authenticated.\n"))

	if event == nil || event.Type != "auth-complete" {
		t.Fatalf("expected auth-complete, got %+v", event)
	}
}

func TestAuthDetector_NoPromptYet_SignalsNothing(t *testing.T) {
	detector := NewAuthDetector()

	event := detector.scan("ws-1", []byte("$ ls\nfile.go\n"))

	if event != nil {
		t.Errorf("expected no event, got %+v", event)
	}
}

func TestAuthDetector_MultipleWorkspaces_AreIndependent(t *testing.T) {
	detector := NewAuthDetector()
	_ = detector.scan("ws-1", []byte("Visit https://claude.ai/device and enter code ABCD-1234\n"))

	event := detector.scan("ws-2", []byte("Visit https://claude.ai/device and enter code WXYZ-5678\n"))

	if event == nil || event.Type != "auth-required" {
		t.Fatalf("ws-2 should independently signal auth-required, got %+v", event)
	}
}
