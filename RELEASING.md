# Releasing perch

Releases are built and published by GitHub Actions. Pushing a semantic version
tag is the only manual step.

## One-time repository setup

Configure these in the GitHub repository settings before cutting the first
release:

1. **Workflow permissions** — *Settings → Actions → General → Workflow
   permissions*: choose **Read repository contents and packages permissions**.
   Each workflow job requests the extra permissions it needs explicitly.
2. **Immutable releases** — *Settings → General → Releases*: enable
   **Immutable releases**. Published tags and assets can then never be
   silently replaced. GoReleaser is designed for this mode.
3. **Release environment** — *Settings → Environments*: create an environment
   named `release`. Add required reviewers if you want a manual approval gate.
   The release workflow targets this environment.
4. **Branch protection** — protect `main` and require the CI checks (`Test`,
   `Lint`, `Vulnerability check`, `Release config and snapshot build`).
5. **Security** — enable Dependabot alerts, Dependabot security updates, and
   private vulnerability reporting. The repository includes a Dependabot
   configuration for Go modules and GitHub Actions.

The workflows pin third-party actions to full commit SHAs. Dependabot keeps
those pins and the Go module graph up to date.

## Versioning

`perch` uses [Semantic Versioning](https://semver.org/). Release tags are
`vMAJOR.MINOR.PATCH`:

- `v0.1.0` — first release, breaking changes are allowed before `v1.0.0`
- `v1.2.3` — normal release
- `v1.2.3-rc.1` — pre-release; GoReleaser marks it as a GitHub pre-release
  automatically

The version embedded in the binary comes from the tag. Do not edit a version
constant for a release.

## Release checklist

1. Make sure `main` is green and up to date:

   ```sh
   git switch main
   git pull --ff-only
   git status --short   # must be empty
   ```

2. Run the local checks:

   ```sh
   make tidy vet race lint
   make release-check
   make snapshot       # cross-builds archives without publishing
   ```

3. Pick the next version and create a signed tag:

   ```sh
   git tag -s v0.1.0 -m "perch v0.1.0"
   git push origin v0.1.0
   ```

   If you have not configured tag signing, use `git tag -a` instead. Signed
   tags are strongly preferred; GitHub shows their verification status.

4. Watch the **Release** workflow:

   ```sh
   gh run list --workflow=release.yml
   gh run watch
   ```

5. Verify the published release:

   ```sh
   gh release view v0.1.0
   ```

## What the release workflow does

The `Release` workflow runs on any `v*` tag push:

1. **Verify tag** — checks that the tag is `vMAJOR.MINOR.PATCH`, points to a
   commit on `main`, and that the module is tidy, vets cleanly, passes
   `go test -race -shuffle=on`, and has no called vulnerabilities according to
   `govulncheck`.
2. **Build and publish** — runs GoReleaser with full history to:
   - cross-compile CGO-free binaries for Linux (amd64, arm64, 386, armv6,
     armv7), macOS (amd64, arm64), and Windows (amd64, arm64);
   - create `.tar.gz` archives (`.zip` on Windows) containing the binary,
     `README.md`, and `LICENSE`;
   - create a source tarball;
   - write `checksums.txt` (SHA-256);
   - generate SPDX SBOMs for every archive and the source tarball with Syft;
   - sign `checksums.txt` with cosign keyless signing;
   - publish the GitHub release with notes generated from conventional commit
     prefixes.
3. **Attest provenance** — publishes a GitHub build provenance attestation for
   every artifact listed in `checksums.txt`.

Both workflows use the latest patched Go 1.26.x toolchain (`go-version: "1.26"`
with `check-latest: true`) rather than the minimum in `go.mod`, so official
binaries include standard-library security fixes.

The job uses only the repository-scoped `GITHUB_TOKEN`; no long-lived release
secrets are stored.

## Verifying a release

Download the assets you need, plus `checksums.txt` and
`checksums.txt.sigstore.json`. To check every downloaded asset:

```sh
# Linux (ignores assets you did not download)
sha256sum --ignore-missing -c checksums.txt

# macOS (download all assets, or check one line at a time)
shasum -a 256 -c checksums.txt
```

Verify the keyless signature on the checksum file:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/synqronlabs/perch/.github/workflows/release.yml@refs/tags/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Verify build provenance for an asset:

```sh
gh attestation verify perch_0.1.0_linux_amd64.tar.gz --repo synqronlabs/perch
```

## When a release goes wrong

- **The workflow failed before creating the GitHub release**: delete the tag,
  fix the problem, and tag again:

  ```sh
  git push --delete origin v0.1.0
  git tag -d v0.1.0
  ```

- **The release was published**: do not move or delete the tag. Cut a new patch
  release (`v0.1.1`) with the fix. Immutable releases and downstream checksum
  caches assume a version never changes meaning.

## Local GoReleaser commands

```sh
goreleaser check                  # validate .goreleaser.yaml
goreleaser build --snapshot --clean
goreleaser release --snapshot --clean --skip=publish,sign,sbom
```

A full local release (including signing and SBOMs) needs `cosign` and `syft` on
`PATH`; the GitHub workflow installs both automatically.
