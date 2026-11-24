// Command citeupdate sets version, date-released, and commit in CITATION.cff
// from a published GitHub Release. .github/workflows/citation.yml runs it.
// It is not an sdf subcommand.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "citation: %s\n", err)
		os.Exit(1)
	}
}

func run(getenv func(string) string) error {
	cfg, err := configFromEnv(getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := Sync(ctx, cfg)
	if err != nil {
		return redact(err, cfg.Token)
	}
	fmt.Println(result.String())
	return nil
}

func configFromEnv(getenv func(string) string) (Config, error) {
	draft, err := parseBool("CITATION_DRAFT", getenv("CITATION_DRAFT"))
	if err != nil {
		return Config{}, err
	}
	pre, err := parseBool("CITATION_PRERELEASE", getenv("CITATION_PRERELEASE"))
	if err != nil {
		return Config{}, err
	}
	api := getenv("GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	u, err := url.Parse(api)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return Config{}, errors.New("GITHUB_API_URL must be an https URL")
	}
	return Config{
		Token:       getenv("GITHUB_TOKEN"),
		Repo:        getenv("GITHUB_REPOSITORY"),
		APIBase:     api,
		Tag:         getenv("CITATION_TAG"),
		PublishedAt: getenv("CITATION_PUBLISHED_AT"),
		Branch:      getenv("CITATION_BRANCH"),
		Draft:       draft,
		Prerelease:  pre,
	}, nil
}

func parseBool(name, v string) (bool, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func redact(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}
