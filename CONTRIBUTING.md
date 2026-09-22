# Contributing

Thank you for your help with this project.

## Before you start

CAUTION: Do not open a public issue for a security weakness. Read
[SECURITY.md](SECURITY.md) instead.

For a change that is more than a fix of a few lines, open an issue first. A
short agreement on the plan saves work for you and for the reviewer.

## Build and test

You need Go 1.25 or later. The module has no private dependency, so a clone
and a build are enough.

```sh
git clone https://github.com/noders-team/x402-celestia-rpc-payer.git
cd x402-celestia-rpc-payer
make build
make test
```

Run these 4 targets before you open a pull request:

```sh
make fmt        # format the source
make vet        # go vet
make lint       # golangci-lint. See https://golangci-lint.run
make test       # the tests, with the race detector
```

The tests use a fake sidecar and a fake chain. They need no network, no node
and no money.

## The shape of the repository

| Directory | What it holds |
|---|---|
| `x402/` | the wire format of the x402 protocol. No chain code. |
| `payer/` | the library: the configuration, the wallet, the signer, the client and the proxy |
| `cmd/x402-celestia-rpc-payer/` | the command line and the commands |

A change of the protocol goes in `x402/`. A change of the payment logic goes
in `payer/`. A new command goes in `cmd/`.

## Style

The code follows the style of the standard library of Go, plus 3 rules:

1. **Write a test for every rejection.** A check that refuses a payment needs
   a test that shows the refusal. Assert the reason of the error with
   `strings.Contains`, and not the error alone.
2. **Say why, not what, in a comment.** The code says what it does. A comment
   says why the code is that way, or what breaks without it.
3. **Do not write the sign bytes by hand.** Use
   `authsigning.GetSignBytesAdapter`. It is the helper that the ante handler
   of the chain uses, so it stays correct for every sign mode.

`payer_test.go` checks each signature again with
`authsigning.GetSignBytesAdapter`. That check is an independent reference for
the signer. Keep it.

## A pull request

1. Make a branch from `main`.
2. Make the change, with a test.
3. Run `make fmt vet lint test`.
4. Write a commit message that says what changed and why.
5. Open the pull request, and name the issue that it closes.

A pull request needs a green CI run and 1 approval.

## The license

The project has the MIT license. Your contribution takes the same license.
