# Third-party licenses

RadarModel is under the Apache License 2.0 (see [LICENSE](LICENSE) and
[NOTICE](NOTICE)). `radarmodel-ai` uses only the Go standard library.
`radarmodel-schema` also contains the database drivers below. Every release
archive carries a THIRD_PARTY_LICENSES.md with the full license text of each
module compiled in, written by
[scripts/third-party-licenses.sh](scripts/third-party-licenses.sh) at build
time.

| Module | Version | License | Used for |
|---|---|---|---|
| github.com/lib/pq | v1.12.3 | MIT | Postgres |
| github.com/go-sql-driver/mysql | v1.10.1 | Mozilla Public License 2.0 | MySQL and MariaDB |
| filippo.io/edwards25519 | v1.2.0 | BSD 3-Clause | MySQL authentication |
| github.com/ncruces/go-sqlite3 | v0.30.5 | MIT; SQLite itself is in the public domain | SQLite |
| github.com/ncruces/julianday | v1.0.0 | MIT | SQLite |
| github.com/tetratelabs/wazero | v1.11.0 | Apache 2.0 | SQLite (runs it as WebAssembly, no CGo) |
| golang.org/x/sys | v0.40.0 | BSD 3-Clause | SQLite |
| The Go runtime and standard library | as go.mod says | BSD 3-Clause | both commands |

The MySQL driver is under the Mozilla Public License 2.0. Its source is at
https://github.com/go-sql-driver/mysql, unmodified.
