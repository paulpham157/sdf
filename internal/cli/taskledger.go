package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/paulpham157/sdf/internal/ledger"
)

// taskLedgerPath is the ledger sdf task and sdf verify use: --ledger PATH,
// else $SDF_LEDGER, else none. It is opt-in. Unlike sdf audit, there is no
// $XDG_DATA_HOME or ~/.local/share default, so with neither set no ledger
// file is opened or created and the commands behave as they did before.
func taskLedgerPath(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv("SDF_LEDGER")
}

// openTaskLedger opens the ledger at path. When write is true it also
// prepares it for appending (creating the file and table if needed), so a
// ledger that cannot be written fails before any task copy is written or
// any check runs. A read-only open never creates the file; a missing file
// reads as empty. path "" returns nil.
func openTaskLedger(ctx context.Context, path string, write bool) (*ledger.Store, error) {
	if path == "" {
		return nil, nil
	}
	st, err := ledger.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ledger %s: %w", ledger.Safe(path), err)
	}
	if write {
		err = st.Prepare(ctx)
	} else if _, herr := st.Head(ctx); herr != nil && !errors.Is(herr, ledger.ErrNotFound) {
		err = herr
	}
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("ledger %s: %s", ledger.Safe(st.Path()), ledger.Safe(err.Error()))
	}
	return st, nil
}
