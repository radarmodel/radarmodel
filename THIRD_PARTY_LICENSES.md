# Third-party licenses

RadarModel is under the Apache License 2.0 (see [LICENSE](LICENSE) and
[NOTICE](NOTICE)). It uses only the Go standard library, so the commands and
their ready-built binaries contain no third-party modules besides the Go
runtime:

| Module | License |
|---|---|
| The Go runtime and standard library | BSD 3-Clause |

Every release archive carries a THIRD_PARTY_LICENSES.md with the full license
text, written by
[scripts/third-party-licenses.sh](scripts/third-party-licenses.sh) at build
time. If a dependency is ever added, that script picks it up and CI fails when
its license file is missing.
