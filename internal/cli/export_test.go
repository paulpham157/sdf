package cli

import "github.com/paulpham157/sdf/internal/task"

// SetTaskLedger points sdf task at l for one test.
func SetTaskLedger(l task.Ledger) func() {
	old := taskLedger
	taskLedger = l
	return func() { taskLedger = old }
}
