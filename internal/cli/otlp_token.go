package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

func resolveServeOTLPToken(tokenFlag string, tokenFlagSet bool, tokenFileFlag string, tokenFileFlagSet bool, getenv func(string) string, stderr io.Writer) (string, int) {
	if tokenFlagSet && tokenFileFlagSet {
		fmt.Fprintln(stderr, "sdf serve: --otlp-token and --otlp-token-file are mutually exclusive")
		return "", 2
	}
	if tokenFlagSet {
		return strings.TrimSpace(tokenFlag), 0
	}
	if tokenFileFlagSet {
		path := strings.TrimSpace(tokenFileFlag)
		if path == "" {
			fmt.Fprintln(stderr, "sdf serve: --otlp-token-file requires a path")
			return "", 2
		}
		return readOTLPTokenFile(path, stderr)
	}
	envToken := strings.TrimSpace(getenv("SDF_OTLP_TOKEN"))
	envFile := strings.TrimSpace(getenv("SDF_OTLP_TOKEN_FILE"))
	if envToken != "" && envFile != "" {
		fmt.Fprintln(stderr, "sdf serve: SDF_OTLP_TOKEN and SDF_OTLP_TOKEN_FILE are mutually exclusive")
		return "", 2
	}
	if envFile != "" {
		return readOTLPTokenFile(envFile, stderr)
	}
	return envToken, 0
}

func readOTLPTokenFile(path string, stderr io.Writer) (string, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "sdf serve: could not read OTLP token file %s: %s\n", displayPath(path), displayErr(err))
		return "", 1
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		fmt.Fprintf(stderr, "sdf serve: OTLP token file %s is empty\n", displayPath(path))
		return "", 1
	}
	if warning := tokenFileModeWarning(path); warning != "" {
		fmt.Fprintln(stderr, warning)
	}
	return token, 0
}

func tokenFileModeWarning(path string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.Mode().Perm()&0o044 == 0 {
		return ""
	}
	return "warning: OTLP token file " + displayPath(path) + " is group- or world-readable"
}
