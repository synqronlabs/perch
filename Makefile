.PHONY: build test race vet lint tidy clean snapshot release-check

# Release builds override this via -ldflags in GoReleaser.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --dirty 2>/dev/null || echo 0.1.0-dev))
LDFLAGS := -s -w -X github.com/synqronlabs/perch/internal/cli.Version=$(VERSION)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o perch ./cmd/perch

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy -diff

# Build release archives and checksums locally without publishing, signing,
# or generating SBOMs (those require cosign/Syft and GitHub OIDC).
# Limit parallelism so this stays usable on smaller laptops.
snapshot:
	GOMAXPROCS=2 goreleaser release --snapshot --clean --parallelism=2 --skip=publish,sign,sbom

release-check:
	goreleaser check

clean:
	rm -f perch
	rm -rf dist
