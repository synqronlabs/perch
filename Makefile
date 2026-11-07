.PHONY: build test race vet lint tidy clean snapshot release-check docs website docs-check skill-check favicons


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

# Regenerate manpages and the website CLI reference.
docs:
	go run ./cmd/gen-docs

# Build the Hugo Book documentation site.
website: docs
	cd website && hugo --minify --gc --cleanDestinationDir

# Fail when generated documentation is stale or new generated files are untracked.
docs-check: docs
	@test -z "$$(git status --porcelain -- man website/content/docs/cli)" || { \
		git status --short -- man website/content/docs/cli; \
		echo "generated docs are stale; run make docs and commit the result" >&2; \
		exit 1; \
	}

# Regenerate the favicon set from website/static/favicon.svg.
favicons:
	./website/scripts/generate-favicons.sh

# Validate the shipped Agent Skill without requiring Node or network access.
skill-check:
	@test "$$(head -n 1 skills/perch/SKILL.md)" = "---"
	@grep -q '^name: perch$$' skills/perch/SKILL.md
	@grep -q '^description: .\+' skills/perch/SKILL.md
	@test -f skills/perch/references/commands.md
	@test -f skills/perch/references/diagnostics.md
	@test -f skills/perch/references/agent-patterns.md
	@test -x skills/perch/scripts/perch-diag.sh
	@sh -n skills/perch/scripts/perch-diag.sh

# Build release archives, checksums, and SBOMs locally without publishing,
# signing, or generating SBOMs (those require cosign/Syft and GitHub OIDC).
# Limit parallelism so this stays usable on smaller laptops.
snapshot:
	GOMAXPROCS=2 goreleaser release --snapshot --clean --parallelism=2 --skip=publish,sign,sbom

release-check:
	goreleaser check

clean:
	rm -f perch
	rm -rf dist website/public website/resources
