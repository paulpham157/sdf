package cli_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/paulpham157/sdf/internal/cli"
)

func TestMain(m *testing.M) {
	cli.SetIdentityRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("identity lookup disabled in tests")
	})
	os.Exit(m.Run())
}
