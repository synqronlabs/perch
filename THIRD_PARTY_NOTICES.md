# Third-Party Notices

perch itself is licensed under **MIT No Attribution (MIT-0)**. See `LICENSE`.

The single binary and source distribution include the third-party modules listed below. Their licenses and notices are reproduced under `third_party/licenses/` and remain in effect. Do not remove that directory from source or binary distributions.

## Apache-2.0 NOTICE files

These components ship `NOTICE` files that must be retained:

- `github.com/aws/aws-sdk-go-v2@v1.47.3`: `third_party/licenses/github.com/aws/aws-sdk-go-v2/NOTICE.txt`
- `github.com/aws/smithy-go@v1.28.5`: `third_party/licenses/github.com/aws/smithy-go/NOTICE`
- `github.com/go-faster/yaml@v0.4.6`: `third_party/licenses/github.com/go-faster/yaml/NOTICE`
- `github.com/synqronlabs/mxraven-go@v0.9.0`: `third_party/licenses/github.com/synqronlabs/mxraven-go/NOTICE`
- `gopkg.in/yaml.v2@v2.4.0`: `third_party/licenses/gopkg.in/yaml.v2/NOTICE`
- `gopkg.in/yaml.v3@v3.0.1`: `third_party/licenses/gopkg.in/yaml.v3/NOTICE`

## Go runtime and standard library

The binary includes the Go runtime and standard library, which are distributed
under the Go BSD-style license and patent grant:

- `third_party/licenses/golang.org/go/LICENSE`
- `third_party/licenses/golang.org/go/PATENTS`

## Embedded third-party notices

- `modernc.org/sqlite` includes SQLite (public domain), sqlite-vec, and other third-party notices. See `third_party/licenses/modernc.org/sqlite/LICENSE-3RD-PARTY.md` and related files.
- `modernc.org/libc` includes third-party notices. See `third_party/licenses/modernc.org/libc/LICENSE-3RD-PARTY.md`.
- `modernc.org/memory` includes Go and mmap-derived notices. See its `LICENSE-GO` and `LICENSE-MMAP-GO` files.
- `gopkg.in/yaml.v2` includes libyaml-derived code under `LICENSE.libyaml`.

## License summary

- Apache-2.0: 24 module(s)
- BSD-3-Clause: 15 module(s)
- MIT: 17 module(s)
- MIT OR Apache-2.0: 3 module(s)
- MIT-0: 1 module(s)

## Module index

| Module | Version | License | License/notice files |
| --- | --- | --- | --- |
| `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` | `v1.7.23` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/config` | `v1.33.9` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/config/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/credentials` | `v1.20.9` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/credentials/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/feature/ec2/imds` | `v1.20.3` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/feature/ec2/imds/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/internal/configsources` | `v1.5.6` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/internal/configsources/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/internal/endpoints/v2` | `v2.8.6` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/internal/endpoints/v2/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/internal/v4a` | `v1.5.6` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/internal/v4a/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding` | `v1.13.21` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/internal/checksum` | `v1.11.7` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/internal/checksum/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/internal/presigned-url` | `v1.14.6` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/internal/presigned-url/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/internal/s3shared` | `v1.20.6` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/internal/s3shared/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/s3` | `v1.114.4` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/s3/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/signin` | `v1.10.4` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/signin/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/ssooidc` | `v1.43.4` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/ssooidc/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/sso` | `v1.38.4` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/sso/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2/service/sts` | `v1.51.4` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/service/sts/LICENSE.txt` |
| `github.com/aws/aws-sdk-go-v2` | `v1.47.3` | Apache-2.0 | `third_party/licenses/github.com/aws/aws-sdk-go-v2/LICENSE.txt`, `third_party/licenses/github.com/aws/aws-sdk-go-v2/NOTICE.txt` |
| `github.com/aws/smithy-go` | `v1.28.5` | Apache-2.0 | `third_party/licenses/github.com/aws/smithy-go/LICENSE`, `third_party/licenses/github.com/aws/smithy-go/NOTICE` |
| `github.com/dlclark/regexp2` | `v1.12.0` | MIT | `third_party/licenses/github.com/dlclark/regexp2/LICENSE` |
| `github.com/dustin/go-humanize` | `v1.1.0` | MIT | `third_party/licenses/github.com/dustin/go-humanize/LICENSE` |
| `github.com/emersion/go-imap/v2` | `v2.0.0-beta.8` | MIT | `third_party/licenses/github.com/emersion/go-imap/v2/LICENSE` |
| `github.com/emersion/go-message` | `v0.18.2` | MIT | `third_party/licenses/github.com/emersion/go-message/LICENSE` |
| `github.com/emersion/go-sasl` | `v0.0.0-20241020182733-b788ff22d5a6` | MIT | `third_party/licenses/github.com/emersion/go-sasl/LICENSE` |
| `github.com/fatih/color` | `v1.19.0` | MIT | `third_party/licenses/github.com/fatih/color/LICENSE.md` |
| `github.com/ghodss/yaml` | `v1.0.0` | BSD-3-Clause | `third_party/licenses/github.com/ghodss/yaml/LICENSE` |
| `github.com/go-faster/errors` | `v0.8.0` | BSD-3-Clause | `third_party/licenses/github.com/go-faster/errors/LICENSE` |
| `github.com/go-faster/jx` | `v1.2.0` | MIT | `third_party/licenses/github.com/go-faster/jx/LICENSE` |
| `github.com/go-faster/yaml` | `v0.4.6` | MIT OR Apache-2.0 | `third_party/licenses/github.com/go-faster/yaml/LICENSE-APACHE`, `third_party/licenses/github.com/go-faster/yaml/LICENSE-MIT`, `third_party/licenses/github.com/go-faster/yaml/NOTICE` |
| `github.com/gofrs/uuid/v5` | `v5.4.0` | MIT | `third_party/licenses/github.com/gofrs/uuid/v5/LICENSE` |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | `third_party/licenses/github.com/google/uuid/LICENSE` |
| `github.com/klauspost/compress` | `v1.20.3` | MIT OR Apache-2.0 | `third_party/licenses/github.com/klauspost/compress/LICENSE` |
| `github.com/mattn/go-colorable` | `v0.1.14` | MIT | `third_party/licenses/github.com/mattn/go-colorable/LICENSE` |
| `github.com/mattn/go-isatty` | `v0.0.24` | MIT | `third_party/licenses/github.com/mattn/go-isatty/LICENSE` |
| `github.com/nats-io/nats.go` | `v1.52.0` | Apache-2.0 | `third_party/licenses/github.com/nats-io/nats.go/LICENSE` |
| `github.com/nats-io/nkeys` | `v0.4.15` | Apache-2.0 | `third_party/licenses/github.com/nats-io/nkeys/LICENSE` |
| `github.com/nats-io/nuid` | `v1.0.1` | Apache-2.0 | `third_party/licenses/github.com/nats-io/nuid/LICENSE` |
| `github.com/ncruces/go-strftime` | `v1.1.0` | MIT | `third_party/licenses/github.com/ncruces/go-strftime/LICENSE` |
| `github.com/ogen-go/ogen` | `v1.24.0` | Apache-2.0 | `third_party/licenses/github.com/ogen-go/ogen/LICENSE` |
| `github.com/philhofer/fwd` | `v1.2.0` | MIT | `third_party/licenses/github.com/philhofer/fwd/LICENSE.md` |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | `third_party/licenses/github.com/remyoudompheng/bigfft/LICENSE` |
| `github.com/segmentio/asm` | `v1.2.1` | MIT-0 | `third_party/licenses/github.com/segmentio/asm/LICENSE` |
| `github.com/shopspring/decimal` | `v1.4.0` | MIT | `third_party/licenses/github.com/shopspring/decimal/LICENSE` |
| `github.com/synqronlabs/mxraven-go` | `v0.9.0` | Apache-2.0 | `third_party/licenses/github.com/synqronlabs/mxraven-go/LICENSE`, `third_party/licenses/github.com/synqronlabs/mxraven-go/NOTICE` |
| `github.com/synqronlabs/raven` | `v1.10.0` | MIT | `third_party/licenses/github.com/synqronlabs/raven/LICENSE` |
| `github.com/tinylib/msgp` | `v1.6.4` | MIT | `third_party/licenses/github.com/tinylib/msgp/LICENSE` |
| `go.uber.org/multierr` | `v1.11.0` | MIT | `third_party/licenses/go.uber.org/multierr/LICENSE.txt` |
| `go.uber.org/zap` | `v1.28.0` | MIT | `third_party/licenses/go.uber.org/zap/LICENSE` |
| `golang.org/x/crypto` | `v0.58.0` | BSD-3-Clause | `third_party/licenses/golang.org/x/crypto/LICENSE` |
| `golang.org/x/exp` | `v0.0.0-20230725093048-515e97ebf090` | BSD-3-Clause | `third_party/licenses/golang.org/x/exp/LICENSE` |
| `golang.org/x/net` | `v0.60.0` | BSD-3-Clause | `third_party/licenses/golang.org/x/net/LICENSE` |
| `golang.org/x/sync` | `v0.24.0` | BSD-3-Clause | `third_party/licenses/golang.org/x/sync/LICENSE` |
| `golang.org/x/sys` | `v0.49.0` | BSD-3-Clause | `third_party/licenses/golang.org/x/sys/LICENSE` |
| `golang.org/x/term` | `v0.47.0` | BSD-3-Clause | `third_party/licenses/golang.org/x/term/LICENSE` |
| `golang.org/x/text` | `v0.43.0` | BSD-3-Clause | `third_party/licenses/golang.org/x/text/LICENSE` |
| `gopkg.in/yaml.v2` | `v2.4.0` | Apache-2.0 | `third_party/licenses/gopkg.in/yaml.v2/LICENSE`, `third_party/licenses/gopkg.in/yaml.v2/LICENSE.libyaml`, `third_party/licenses/gopkg.in/yaml.v2/NOTICE` |
| `gopkg.in/yaml.v3` | `v3.0.1` | MIT OR Apache-2.0 | `third_party/licenses/gopkg.in/yaml.v3/LICENSE`, `third_party/licenses/gopkg.in/yaml.v3/NOTICE` |
| `modernc.org/libc` | `v1.79.0` | BSD-3-Clause | `third_party/licenses/modernc.org/libc/LICENSE`, `third_party/licenses/modernc.org/libc/LICENSE-3RD-PARTY.md` |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause | `third_party/licenses/modernc.org/mathutil/LICENSE` |
| `modernc.org/memory` | `v1.12.1` | BSD-3-Clause | `third_party/licenses/modernc.org/memory/LICENSE`, `third_party/licenses/modernc.org/memory/LICENSE-GO`, `third_party/licenses/modernc.org/memory/LICENSE-LOGO`, `third_party/licenses/modernc.org/memory/LICENSE-MMAP-GO` |
| `modernc.org/sqlite` | `v1.61.0` | BSD-3-Clause | `third_party/licenses/modernc.org/sqlite/LICENSE`, `third_party/licenses/modernc.org/sqlite/LICENSE-3RD-PARTY.md`, `third_party/licenses/modernc.org/sqlite/LICENSE-SQLITE`, `third_party/licenses/modernc.org/sqlite/LICENSE-SQLITE_VEC` |
