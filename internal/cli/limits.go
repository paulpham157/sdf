package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/netlimit"
)

// limitFlags are the resource-limit flags for sdf serve. An unset flag
// falls through to the matching SDF_* variable. An empty variable means
// the default. A flag that is present wins, including when its value is
// invalid: the invalid flag is an error and the variable is not used.
type limitFlags struct {
	maxConns, maxConnsPerIP, maxConnsPerToken string
	maxBody, maxInFlight                      string
	header, read, idle                        string
	maxConnsSet, maxConnsPerIPSet             bool
	maxConnsPerTokenSet                       bool
	maxBodySet, maxInFlightSet                bool
	headerSet, readSet, idleSet               bool
}

func limitFlagNames() []string {
	// Longer names first so a prefix check cannot be added later and
	// match --max-conns inside --max-conns-per-ip. The matcher compares
	// the whole flag, not a prefix.
	return []string{
		"--max-conns-per-token",
		"--max-conns-per-ip",
		"--max-conns",
		"--max-body-bytes",
		"--max-inflight-bytes",
		"--read-header-timeout",
		"--read-timeout",
		"--idle-timeout",
	}
}

// takeLimitFlag reports whether arg is a resource-limit flag. A flag
// without its value is an error.
func takeLimitFlag(arg string, i *int, args []string) (name, val string, ok bool, err error) {
	for _, name := range limitFlagNames() {
		if arg == name {
			v, got := take(i, args)
			if !got {
				return name, "", true, fmt.Errorf("%s requires a value", name)
			}
			return name, v, true, nil
		}
		if strings.HasPrefix(arg, name+"=") {
			return name, strings.TrimPrefix(arg, name+"="), true, nil
		}
	}
	return "", "", false, nil
}

func (f *limitFlags) set(name, val string) {
	switch name {
	case "--max-conns":
		f.maxConns, f.maxConnsSet = val, true
	case "--max-conns-per-ip":
		f.maxConnsPerIP, f.maxConnsPerIPSet = val, true
	case "--max-conns-per-token":
		f.maxConnsPerToken, f.maxConnsPerTokenSet = val, true
	case "--max-body-bytes":
		f.maxBody, f.maxBodySet = val, true
	case "--max-inflight-bytes":
		f.maxInFlight, f.maxInFlightSet = val, true
	case "--read-header-timeout":
		f.header, f.headerSet = val, true
	case "--read-timeout":
		f.read, f.readSet = val, true
	case "--idle-timeout":
		f.idle, f.idleSet = val, true
	}
}

func (f limitFlags) resolve(getenv func(string) string) (netlimit.Limits, error) {
	var out netlimit.Limits
	var err error
	if out.MaxConns, err = pickPositive(f.maxConnsSet, f.maxConns, "--max-conns", "SDF_MAX_CONNS", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	if out.MaxConnsPerIP, err = pickPositive(f.maxConnsPerIPSet, f.maxConnsPerIP, "--max-conns-per-ip", "SDF_MAX_CONNS_PER_IP", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	if out.MaxConnsPerToken, err = pickPositive(f.maxConnsPerTokenSet, f.maxConnsPerToken, "--max-conns-per-token", "SDF_MAX_CONNS_PER_TOKEN", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	if out.MaxBodyBytes, err = pickPositive(f.maxBodySet, f.maxBody, "--max-body-bytes", "SDF_MAX_BODY_BYTES", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	inflight, err := pickPositive(f.maxInFlightSet, f.maxInFlight, "--max-inflight-bytes", "SDF_MAX_INFLIGHT_BYTES", getenv)
	if err != nil {
		return netlimit.Limits{}, err
	}
	out.MaxInFlightBytes = int64(inflight)
	if out.ReadHeaderTimeout, err = pickDuration(f.headerSet, f.header, "--read-header-timeout", "SDF_READ_HEADER_TIMEOUT", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	if out.ReadTimeout, err = pickDuration(f.readSet, f.read, "--read-timeout", "SDF_READ_TIMEOUT", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	if out.IdleTimeout, err = pickDuration(f.idleSet, f.idle, "--idle-timeout", "SDF_IDLE_TIMEOUT", getenv); err != nil {
		return netlimit.Limits{}, err
	}
	return netlimit.Resolve(out)
}

func pickPositive(set bool, flag, flagName, envKey string, getenv func(string) string) (int, error) {
	if set {
		n, err := parsePositiveDecimal(flag)
		if err != nil {
			return 0, fmt.Errorf("%s requires a positive decimal number", flagName)
		}
		return n, nil
	}
	v := getenv(envKey)
	if strings.TrimSpace(v) == "" {
		return 0, nil
	}
	n, err := parsePositiveDecimal(v)
	if err != nil {
		return 0, fmt.Errorf("%s requires a positive decimal number", envKey)
	}
	return n, nil
}

func pickDuration(set bool, flag, flagName, envKey string, getenv func(string) string) (time.Duration, error) {
	if set {
		d, err := parsePositiveDuration(flag)
		if err != nil {
			return 0, fmt.Errorf("%s requires a positive duration (for example 5s)", flagName)
		}
		return d, nil
	}
	v := getenv(envKey)
	if strings.TrimSpace(v) == "" {
		return 0, nil
	}
	d, err := parsePositiveDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s requires a positive duration (for example 5s)", envKey)
	}
	return d, nil
}

// parsePositiveDecimal accepts a plain decimal integer with no sign, no
// leading zero, and no spaces. Zero is rejected so a limit cannot be
// turned off.
func parsePositiveDecimal(v string) (int, error) {
	if v == "" || (len(v) > 1 && v[0] == '0') {
		return 0, strconv.ErrSyntax
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	n, err := strconv.ParseInt(v, 10, 0)
	if err != nil || n < 1 {
		return 0, strconv.ErrSyntax
	}
	return int(n), nil
}

func parsePositiveDuration(v string) (time.Duration, error) {
	if v == "" || strings.TrimSpace(v) != v {
		return 0, strconv.ErrSyntax
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, strconv.ErrSyntax
	}
	return d, nil
}
