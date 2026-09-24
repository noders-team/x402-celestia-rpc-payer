# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Fixed

- **A lost answer no longer makes the budget count less than the wallet
  spent.** When the answer of the sidecar does not arrive, the payer does not
  know if the chain took the payment. It now keeps the payment open, and it
  asks the free `GET /x402/tx` route of the sidecar before it signs the next
  payment. A payment that reached a block stands, and a payment that the
  chain does not hold goes back. The same answer picks the right account
  sequence, so the next payment does not clash with a transaction that the
  chain already holds.

### Added

- Each GitHub Release has binaries for Linux and macOS, on amd64 and arm64,
  with `SHA256SUMS` and a build attestation. The prerelease of a release
  candidate has them too. The README tells how to download and check them.
- The `-version` flag, also as `--version`. It prints the same line as the
  `version` command.
- `make dist` builds the release binaries and `SHA256SUMS` into `dist/`.
- `Payment.TxHash` holds the hash of the bytes that the payer signed. The
  payer computes it, so it knows the hash before the sidecar broadcasts the
  transaction.
- `Payer.Unresolved` records a payment whose answer did not arrive, and
  `Payer.Open` lists them.
- `GET /x402/payer/status` names each open payment in its `unresolved` field.

### Changed

- `NewPayer` takes a `Chain` in place of an `AccountFunc`. `Chain` has
  `Account` and `TxStatus`, and `Accounts` satisfies it. A caller writes
  `payer.NewPayer(cfg, wallet, accounts)` in place of
  `payer.NewPayer(cfg, wallet, accounts.Account)`.
- `Payer.Rollback` no longer runs when the answer does not arrive. It runs
  only when the sidecar answered and refused the payment, because the sidecar
  broadcast nothing in that case.

### Security

- The build uses Go 1.26.8 through the `toolchain` line of `go.mod`. Go 1.26.5
  has 6 known vulnerabilities in `net/http`, `net/url`, `crypto/tls`,
  `html/template` and `encoding/asn1`. A program that imports the `payer`
  package can still use Go 1.26.5.
- `google.golang.org/grpc` v1.82.1 -> v1.83.1 (GO-2026-6348) and
  `github.com/pion/dtls/v3` v3.1.2 -> v3.1.4 (GO-2026-6165). Both are
  indirect.

### Requires

- The sidecar must serve the free route `GET /x402/tx?hash=…`. An older
  sidecar leaves each lost payment open, and the payer counts it against the
  budget until it stops.

## 0.1.0

The first public release.

### Added

- `serve`: a local CometBFT RPC proxy that pays for each call that needs a
  payment.
- `call`: 1 paid RPC call from the command line, in the URI style or in the
  JSON-RPC style.
- `init`: it reads the price list of the sidecar, writes `config.yaml`, and
  makes a wallet in `.mnemonic` with the mode 0600.
- `fund`: it shows the address of the payer and waits for the money.
- `address`, `balance`, `prices`, `health`, `check` and `version`.
- The `-no-pay` flag, which stops every payment and gives the 402 answer to
  the caller.
- The `payer` package, which other Go programs can import.
- The `x402` package, which holds the wire format of the protocol.

### Security

- The payer refuses a price above `maxPerCall`.
- The payer stops when the sum of the prices and the fees reaches `budget`.
- The payer pays 1 token and 1 network only.
- The payer refuses a recipient that is not a `celestia1…` address.
- The payer sends a call 2 times at most. It does not sign a second payment
  after a refusal.
