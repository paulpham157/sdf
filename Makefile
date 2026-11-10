# Build a static sdf binary. CGO is off so the SQLite driver stays pure Go.
BINARY ?= sdf

# staticcheck 2026.2.2. CI installs that GitHub release and checks its sha256;
# this target runs the same release as a module.
STATICCHECK_MODULE := honnef.co/go/tools/cmd/staticcheck@v0.8.2

.PHONY: build test vet vuln clean fmt-check staticcheck race e2e

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/sdf

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "gofmt -l:"; \
		echo "$$files"; \
		exit 1; \
	fi

staticcheck:
	go run $(STATICCHECK_MODULE) -tags e2e ./...

race:
	go test -race ./...

# Post-merge check. Builds the static binary, then receives tests/samples.
e2e: build
	SDF_BIN=$(abspath $(BINARY)) go test -tags e2e -count=1 -timeout 5m ./tests/e2e

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

clean:
	rm -f $(BINARY)
