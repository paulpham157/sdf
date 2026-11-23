package task

import "github.com/paulpham157/sdf/internal/ledger"

// The ledger is internal/ledger (#189). These aliases keep the task and
// verify packages' names, so callers do not change, while every type, error,
// and hash rule is the ledger package's own. There is no second chain
// implementation here.
type (
	// Kind is the record type and the hash domain, for example "sdf.task/v1".
	Kind = ledger.Kind
	// Entry is the ledger's receipt for one appended payload.
	Entry = ledger.Entry
	// Ledger is an append-only store of canonical payloads. ledger.Store
	// implements it.
	Ledger = ledger.Ledger
	// BatchLedger chains several payloads in one transaction, all or none.
	// ledger.Store implements it.
	BatchLedger = ledger.BatchLedger
	// Item is one payload for BatchLedger.AppendAll.
	Item = ledger.Item
)

// Record kinds.
const (
	KindTask     Kind = "sdf.task/v1"
	KindEvidence Kind = "sdf.evidence/v1"
	KindVerdict  Kind = "sdf.verdict/v1"
)

// Ledger errors, from the ledger package.
var (
	ErrHashMismatch = ledger.ErrHashMismatch
	ErrConflict     = ledger.ErrConflict
	ErrNotFound     = ledger.ErrNotFound
	ErrInvalidID    = ledger.ErrInvalidID
)

// TimeLayout is the ledger's UTC timestamp form, nine fraction digits.
const TimeLayout = ledger.TimeLayout

// PayloadHash is "sha256:" + hex(sha256(kind + "\n" + canonical)), the
// ledger's payload hash.
func PayloadHash(kind Kind, canonical []byte) string {
	return ledger.PayloadHash(kind, canonical)
}
