# Build a static sdf binary. CGO is off so the SQLite driver stays pure Go.
BINARY ?= sdf

.PHONY: build test vet vuln clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/sdf

test:
	go test ./...

vet:
	go vet ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

clean:
	rm -f $(BINARY)
