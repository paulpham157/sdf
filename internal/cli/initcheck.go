package cli

import (
	"os"
	"strings"
)

// hookToken returns the token the sdf session and git hooks would send, the
// variable it came from, and a note when they would send none although a
// variable is set. It follows the hooks: SDF_OTLP_TOKEN, else the file named
// by SDF_OTLP_TOKEN_FILE, trimmed at the ends; both set sends nothing.
func hookToken(getenv func(string) string) (token, source, note string) {
	envToken := strings.TrimSpace(getenv("SDF_OTLP_TOKEN"))
	envFile := strings.TrimSpace(getenv("SDF_OTLP_TOKEN_FILE"))
	if envToken != "" && envFile != "" {
		return "", "", "SDF_OTLP_TOKEN and SDF_OTLP_TOKEN_FILE are both set; the sdf hooks send no token then.\n"
	}
	if envToken != "" {
		return envToken, "SDF_OTLP_TOKEN", ""
	}
	if envFile != "" {
		data, err := os.ReadFile(envFile)
		if err != nil {
			return "", "", "SDF_OTLP_TOKEN_FILE " + envFile + " cannot be read; the sdf hooks send no token.\n"
		}
		if t := strings.TrimSpace(string(data)); t != "" {
			return t, "SDF_OTLP_TOKEN_FILE", ""
		}
		return "", "", "SDF_OTLP_TOKEN_FILE " + envFile + " is empty; the sdf hooks send no token.\n"
	}
	return "", "", ""
}
