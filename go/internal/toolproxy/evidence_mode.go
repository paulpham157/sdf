package toolproxy

import "fmt"

// Evidence modes are ADR 0031's only value set. live means a real sandbox
// existed for the Attempt. It is not production impact (ADR 0006).
const (
	EvidenceModeSynthetic = "synthetic"
	EvidenceModeLive      = "live"
)

// EvidenceModeSource reads the Attempt's stored mode. ok is false when no
// Attempt record exists. A non-nil error is a store failure; the caller
// must not guess a mode.
type EvidenceModeSource func(attemptID string) (mode string, ok bool, err error)

// StampEvidenceMode is ADR 0031's read rule: only an exact "live" is live.
// A missing Attempt, an empty string, and any unknown value are synthetic.
func StampEvidenceMode(stored string) string {
	if stored == EvidenceModeLive {
		return EvidenceModeLive
	}
	return EvidenceModeSynthetic
}

// normalizeWriteMode is the write rule. Empty means the writer did not
// claim a mode and is stored as synthetic. Any other unknown value is
// refused so the fence never persists a mode outside the ADR 0031 set.
func normalizeWriteMode(mode string) (string, error) {
	switch mode {
	case "", EvidenceModeSynthetic:
		return EvidenceModeSynthetic, nil
	case EvidenceModeLive:
		return EvidenceModeLive, nil
	default:
		return "", fmt.Errorf("toolproxy: ADR 0031: evidence_mode %q is not synthetic or live", mode)
	}
}
