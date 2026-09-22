# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
