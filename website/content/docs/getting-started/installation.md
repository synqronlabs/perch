---
title: Installation
weight: 10
---

Prebuilt binaries for Linux, macOS, and Windows are published on the
[releases page](https://github.com/synqronlabs/perch/releases). Every release
includes SHA-256 checksums, SBOMs, a keyless cosign signature, and GitHub build
provenance. See
[RELEASING.md](https://github.com/synqronlabs/perch/blob/main/RELEASING.md#verifying-a-release)
for verification details.

## With Go

If you have Go 1.26.4 or later, `go install` builds the latest tagged release.
CGO is not required.

```sh
go install github.com/synqronlabs/perch/cmd/perch@latest
```

The binary is written to `$(go env GOPATH)/bin` (usually `~/go/bin`). Add that
directory to `PATH` if needed. To pin a version:

```sh
go install github.com/synqronlabs/perch/cmd/perch@v0.2.0
```

## From the latest GitHub release

This downloads the newest published release for your OS and architecture and
installs it to `~/.local/bin`. It works without a Go toolchain.

```sh
# Resolve the latest release tag (for example v0.2.0).
version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
  https://github.com/synqronlabs/perch/releases/latest | sed 's#.*/tag/v##')
[ -n "$version" ] || { echo "could not determine the latest release" >&2; exit 1; }

# Pick the matching archive for this machine.
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  armv6l) arch=armv6 ;;
  armv7l) arch=armv7 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

archive="perch_${version}_${os}_${arch}.tar.gz"
curl -fsSLO "https://github.com/synqronlabs/perch/releases/download/v${version}/${archive}"
tar -xzf "${archive}"
mkdir -p ~/.local/bin
install -m 0755 "perch_${version}_${os}_${arch}/perch" ~/.local/bin/perch
```

Make sure `~/.local/bin` is on `PATH`, then check the install:

```sh
perch version
```

To install system-wide instead, replace the last two lines with:

```sh
sudo install -m 0755 "perch_${version}_${os}_${arch}/perch" /usr/local/bin/perch
```

Windows users can download the `.zip` archive for their architecture from the
[releases page](https://github.com/synqronlabs/perch/releases).

## From source

Requires Go 1.26.4 or later:

```sh
CGO_ENABLED=0 go build -trimpath -o perch ./cmd/perch
```

## Manpages

Release archives include `man/*.1` pages. To install them system-wide:

```sh
sudo install -d /usr/local/share/man/man1
sudo install -m 0644 man/perch*.1 /usr/local/share/man/man1/
sudo mandb   # on systems that use mandb
```

Then run `man perch`, `man perch-mail`, or `man perch-mail-send`.
