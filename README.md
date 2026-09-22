# x402-celestia-rpc-payer

[![CI](https://github.com/noders-team/x402-celestia-rpc-payer/actions/workflows/ci.yml/badge.svg)](https://github.com/noders-team/x402-celestia-rpc-payer/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/noders-team/x402-celestia-rpc-payer.svg)](https://pkg.go.dev/github.com/noders-team/x402-celestia-rpc-payer)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Pay for Celestia RPC calls with TIA, through the
[x402](https://www.x402.org/) payment protocol.

An x402 sidecar is an HTTP server in front of a Celestia node. It asks for a
payment in TIA before it passes a call to that node. This program answers that
ask. It signs a bank `MsgSend`, puts it in the `Payment-Signature` header, and
sends the call again.

```
your tool ──▶ x402-celestia-rpc-payer :26659 ──▶ x402 sidecar :26658 ──▶ celestia-appd :26657
                                                          │
                                                          └──▶ Celestia chain
```

The payer reaches 1 address: the sidecar. It has no node, no endpoint and no
chain connection of its own.

The payer has 2 shapes:

- **A local proxy.** A tool connects to it in the same way that it connects to
  a CometBFT RPC. The proxy pays for each call that needs a payment.
- **A command line.** It does 1 call, or it reads the wallet and the sidecar.

The payer signs the payment. It does not broadcast it. The sidecar broadcasts
the transaction after the sidecar verifies the payment.

WARNING: This program signs payments with the key of a wallet that has money.
Read [SECURITY.md](SECURITY.md) before you put real TIA in a wallet that it
controls.

## Install

You need Go 1.25 or later.

```sh
go install github.com/noders-team/x402-celestia-rpc-payer/cmd/x402-celestia-rpc-payer@latest
```

Or build it from the source:

```sh
git clone https://github.com/noders-team/x402-celestia-rpc-payer.git
cd x402-celestia-rpc-payer
make build          # -> bin/x402-celestia-rpc-payer
```

The module has only public dependencies, so the build needs no credentials.

## Start

1. Write the configuration and make a wallet.

   ```sh
   x402-celestia-rpc-payer init http://your-sidecar:26658
   ```

   `init` reads the sidecar, so the file that it writes is correct on the first
   try. It writes `config.yaml` and `.mnemonic`, both with the mode 0600, and
   it prints the address of the new wallet.

   WARNING: `.mnemonic` holds the key of a wallet that has money. Both files
   are in `.gitignore`. Keep them out of git.

2. Put TIA in the wallet.

   ```sh
   x402-celestia-rpc-payer fund
   ```

   `fund` shows the address and what a call costs, then it waits until the
   money arrives.

   On the Mocha testnet, ask for test TIA in the `#mocha-faucet` channel of
   the Celestia Discord.

3. Do 1 paid call.

   ```sh
   x402-celestia-rpc-payer call block height=100
   ```

4. Start the local proxy.

   ```sh
   x402-celestia-rpc-payer serve
   ```

   Then point your tool at `http://localhost:26659`.

## Commands

| Command | What it does |
|---|---|
| `init [sidecar]` | write `config.yaml` and a wallet |
| `fund` | show the address, and wait for the money when the wallet is empty |
| `serve` | start the local proxy that pays for each call |
| `call <method> [k=v …]` | do 1 call and print the answer |
| `address` | print the address of the payer wallet |
| `balance` | print the balance of the payer wallet |
| `prices` | print the price of each method of the sidecar |
| `health` | check the sidecar |
| `check` | read the configuration and print it. With `-no-pay` it reads no wallet. |
| `version` | print the version |

Flags:

| Flag | What it does |
|---|---|
| `-config` | the path of the YAML configuration file, default `config.yaml` |
| `-log-level` | `debug`, `info`, `warn`, or `error`, default `info` |
| `-json` | `call`: use a JSON-RPC body instead of the path |
| `-no-pay` | `serve`, `call` and `check`: do not pay, and give the 402 answer to the caller |
| `-force` | `init`: write over a `config.yaml` that exists |

### call

`call` takes the RPC method and its parameters as `key=value` pairs.

```sh
x402-celestia-rpc-payer call status
x402-celestia-rpc-payer call block height=100
x402-celestia-rpc-payer call tx_search query=tx.height=5 page=1
x402-celestia-rpc-payer -json call block height=100
```

NOTE: The URI style of CometBFT needs quotation marks around a string value.
`call` adds them when the value is not a number, not a boolean, and not a `0x`
hash. Write `query=tx.height=5`, and the payer sends `query="tx.height=5"`.

The answer goes to stdout. The price and the transaction hash go to stderr, so
a pipe gets the JSON alone.

```sh
x402-celestia-rpc-payer call block height=100 | jq .result.block.header.time
```

## The local proxy

The proxy carries every path to the sidecar. It owns the `/x402/payer/` prefix:

| Route | What it gives |
|---|---|
| `GET /x402/payer/health` | the state of the proxy |
| `GET /x402/payer/status` | the address of the payer, the sum that it paid, the limits, and each payment that it cannot resolve |

`/x402/health` and `/x402/prices` go to the sidecar, which answers them.

```sh
curl "http://localhost:26659/block?height=100"
curl http://localhost:26659/x402/payer/status
```

NOTE: The proxy does not carry `/websocket`. A websocket has no place for a
payment header, so a subscription cannot pay. Connect to a node directly for a
subscription.

## How a paid call works

1. The proxy sends the call to the sidecar.
2. The sidecar answers `402` with the payment options.
3. The payer picks the option of its network, and checks the price against
   `maxPerCall` and `budget`.
4. The payer asks the sidecar at `/x402/account` for the account number and the
   sequence of the payer. Both are part of the signed bytes.
5. The payer signs a bank `MsgSend` with `SIGN_MODE_DIRECT`. The chain-id is
   part of the signed bytes, so the payment holds for 1 chain only.
6. The payer sends the call again with the `Payment-Signature` header.
7. The sidecar verifies the payment, broadcasts it, and gives the answer.

The payer sends the call 2 times at most. If the sidecar answers `402` again,
the payer gives that answer to the caller. It does not sign a second payment.

## Safety

| Rule | Where |
|---|---|
| The payer refuses a price above the limit. | `maxPerCall` |
| The payer stops when the sum of the prices and the fees reaches the budget. | `budget` |
| The payer pays 1 token only. It refuses another `asset`. | `asset` |
| The payer pays on 1 network only. | `network` |
| The payer refuses an address of another chain. | the fixed prefix `celestia` |
| The `-no-pay` flag stops every payment. | the `-no-pay` flag |

The budget covers the prices and the fees, because both leave the wallet. The
budget starts at 0 again each time the payer starts. It is not written to a
file.

WARNING: `.mnemonic` holds the mnemonic of a wallet that has money. `init`
writes it there with the mode 0600, next to `config.yaml`. The field
`mnemonic:` of `config.yaml` is the other way, and that file then needs the
mode 0600 too. The payer writes a warning in the log when other users can read
the mnemonic file.

[SECURITY.md](SECURITY.md) holds the complete list, and it says how to report
a weakness.

## The account sequence

The account sequence is part of the signed bytes, so 2 payments must not use
the same sequence. The payer holds a lock while it pays, so 2 paid calls do
not run at the same time. It keeps the next sequence in memory, because the
chain does not know a payment before the payment reaches a block.

CAUTION: 2 payers that use the same mnemonic take the same sequence, and 1 of
the 2 payments fails. Run `init` in a second directory to give each payer its
own wallet.

## Configuration

`init` writes 4 fields. They are the 4 decisions that only you can make.

CAUTION: The fee of a Celestia transaction uses the denom of the payment, and
the chain takes a fee in `utia` only. `init` stops with an error when the
sidecar asks for another token, and it writes no file.

```yaml
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
maxPerCall: "20000"
budget: "0"
```

| Field | What it is |
|---|---|
| `upstream` | the sidecar that the payer pays |
| `network` | the CAIP-2 network. It must match the sidecar. |
| `maxPerCall` | the highest price of 1 call, in utia |
| `budget` | the highest sum while the payer runs. It covers the prices and the fees. `"0"` removes the limit. |

Every other field has a good default, and the written file leaves it out.
[`config.example.yaml`](config.example.yaml) lists all of them with a comment
for each: `listen`, `mnemonicFile`, `mnemonic`, `asset`, `fee`, `gasLimit`,
`timeoutSeconds` and `memo`.

The BIP-44 path is fixed at `m/44'/118'/0'/0/0`, and the address prefix is
fixed at `celestia`.

### Networks

| Network | `network` | Denom |
|---|---|---|
| Mainnet | `cosmos:celestia` | `utia` |
| Mocha testnet | `cosmos:mocha-5` | `utia` |

TIA has 6 decimals, so 1000 utia is 0.001 TIA.

CAUTION: A Celestia testnet gets a new chain-id when it restarts. The chain-id
is part of the signed bytes, so a payment for the old chain-id is rejected.
Run `init` again, or change `network` by hand.

### The payer needs no node

The payer reads 2 numbers before it signs: the account number and the sequence
of the payer. The chain puts both in the signed bytes, so the payer cannot
make them up.

The payer asks the sidecar for them, at the free route `GET /x402/account`. The
sidecar already holds a connection to a node, because it verifies and
broadcasts every payment. So you configure no endpoint, and the payer carries
no chain connection.

```sh
curl "http://localhost:26658/x402/account?address=celestia1…"
```

```json
{
  "address": "celestia1…",
  "network": "cosmos:mocha-5",
  "chainId": "mocha-5",
  "accountNumber": "42",
  "sequence": "7",
  "denom": "utia",
  "balance": "499985000"
}
```

The payer refuses an answer whose `network` is not its own network.

NOTE: A hostile sidecar can give wrong numbers. It cannot take money that way.
Wrong numbers make the signature invalid, and the chain then rejects the
payment. The price, the token and the recipient come from the checks of the
payer, not from this answer.

CAUTION: The payer needs a sidecar with the `/x402/account` route. A sidecar
without it cannot tell the payer what to sign. It needs the `/x402/tx` route
too. The section [A lost answer](#a-lost-answer) says why.

## A lost answer

The payer signs a payment, and the sidecar broadcasts it. Between those 2
steps the answer can go missing: the connection breaks, the sidecar stops, or
the time limit ends the wait. The payer then does not know if the chain took
the money.

The payer must not guess. A wrong guess costs 2 things:

- **The budget.** If the payer takes the payment back and the chain kept it,
  the budget counts less than the wallet spent, and it stops too late.
- **The account sequence.** The sequence is part of the signed bytes. If the
  payer takes the sequence back and the chain kept it, the next payment signs
  a sequence that the chain already holds, and the chain rejects it.

So the payer asks the chain. It computes the hash of the bytes that it signs,
which is what names a transaction on a Cosmos chain. After a lost answer it
keeps the payment open, and it reads the free `GET /x402/tx` route of the
sidecar before it signs the next payment.

| What the chain says | What the payer does |
|---|---|
| The transaction is in a block, and its code is 0 | The payment stands. The money left, and the chain holds the sequence. |
| The transaction is in a block, and its code is not 0 | The fee left, and the payment did not move. The payer gives the price back and keeps the sequence. |
| The chain holds no such transaction | Nothing left the wallet. The payer gives the price and the fee back, and it reads the sequence of the chain again. |
| The sidecar does not answer | The payer cannot tell. It keeps the payment against the budget, and it asks again on the next payment. |

A transaction needs a block, so the first answer of the chain is often "not
there". The payer therefore asks for about 2 blocks before it decides that
the chain holds nothing. That wait happens 1 time, after a lost answer.

`GET /x402/payer/status` names each open payment in its `unresolved` field:

```json
{
  "payer": "celestia1…",
  "spent": "6000",
  "payments": 2,
  "unresolved": ["1A2B3C…"]
}
```

CAUTION: The payer needs a sidecar with the `/x402/tx` route. An older
sidecar gives no answer there, so each lost payment stays open and counts
against the budget until the payer stops.

## Use it as a Go library

```sh
go get github.com/noders-team/x402-celestia-rpc-payer
```

The repository holds 2 importable packages:

| Package | What it holds |
|---|---|
| [`payer`](payer/) | the configuration, the wallet, the signer, the client and the proxy |
| [`x402`](x402/) | the wire format of the protocol. No chain code. |

```go
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

func main() {
	cfg, err := payer.LoadConfig("config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}

	accounts := payer.NewAccounts(cfg.Upstream, cfg.Network)
	signer := payer.NewPayer(cfg, wallet, accounts)
	client := payer.NewClient(cfg, signer, slog.Default())

	// 1 paid call.
	res, receipt, err := client.Do(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, cfg.Upstream+"/block?height=100", nil)
	})
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	log.Println("paid:", receipt.Paid, receipt.Amount, receipt.Asset)
}
```

The full reference is on
[pkg.go.dev](https://pkg.go.dev/github.com/noders-team/x402-celestia-rpc-payer).

## Container

```sh
docker build --build-arg VERSION=v0.1.0 -t x402-celestia-rpc-payer .
docker run --rm -p 26659:26659 \
  -v "$PWD/config.yaml:/etc/x402-celestia-rpc-payer/config.yaml:ro" \
  -v "$PWD/.mnemonic:/etc/x402-celestia-rpc-payer/.mnemonic:ro" \
  x402-celestia-rpc-payer
```

WARNING: Both files hold the key of a wallet that has money. Mount them
read-only, and give them the mode 0600 on the host.

## Tests

```sh
make vet test
```

The tests use a fake sidecar and a fake chain. They need no network and no
money. `payer/payer_test.go` checks each signature again with
`authsigning.GetSignBytesAdapter`, which is the helper that the ante handler
of the chain uses.

## Known limits

- The proxy does not carry `/websocket`.
- A payment that the payer cannot resolve, because the sidecar stays down,
  counts against the budget until the payer stops. That is the safe
  direction: the budget ends early, and never late. The section
  [A lost answer](#a-lost-answer) says more.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT. See [LICENSE](LICENSE).

The protocol types of `x402/` come from the x402-go library, which carries the
MIT license too. The NOTICE section of `LICENSE` holds its copyright.
