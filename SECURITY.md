# Security

## The software holds money

WARNING: This program signs payments with the key of a wallet that has money.
It has no security audit. Read this file before you put real TIA in a wallet
that it controls.

## Report a weakness

Do not open a public issue for a weakness.

Use the **Report a vulnerability** button of the Security tab of the
repository. GitHub sends the report to the maintainers alone.

Give these items in the report:

1. What an attacker can do.
2. The steps that show it.
3. The version, which `x402-celestia-rpc-payer version` prints.

We answer inside 7 days. We tell you the plan and the date of the fix.

## What the payer protects

| Rule | Where |
|---|---|
| The payer refuses a price above the limit of 1 call. | `maxPerCall` |
| The payer stops when the sum of the prices and the fees reaches the budget. | `budget` |
| The payer pays 1 token only. It refuses another `asset`. | `asset` |
| The payer pays on 1 network only. It refuses another chain-id. | `network` |
| The payer refuses a recipient that is not a `celestia1…` address. | the fixed prefix |
| The `-no-pay` flag stops every payment. | the `-no-pay` flag |

The chain-id and the account number are part of the signed bytes, so 1 signed
payment holds for 1 chain and 1 account.

## The sidecar is not trusted

The payer reads the account number and the sequence of the payer from the
sidecar. A hostile sidecar can give wrong numbers. It cannot take money that
way: wrong numbers make the signature invalid, and the chain then rejects the
payment.

The price, the token and the recipient come from the checks above, not from
the answer of the sidecar.

## The key

The payer reads the mnemonic of a BIP-39 wallet from `.mnemonic`, or from the
`mnemonic` field of the configuration file. `init` writes `.mnemonic` with the
mode 0600.

WARNING: The private key stays in the memory of the process while it runs. Run
the payer on a machine that you control. Do not run 2 payers with the same
mnemonic: both take the same account sequence, and 1 of the 2 payments fails.

The payer never writes the mnemonic to the terminal, to a log, or to an error
message.

## The dependencies

CI runs `govulncheck` on each push. The job reports, but it does not block a
merge.

NOTE: cosmos-sdk reaches `golang.org/x/crypto/openpgp` through its client
package. The advisory GO-2026-5932 says that the package is unmaintained, and
it has no fix. A blocking job would be red on every run, and nobody would read
it. The payer does not open a PGP message.

Run the scan yourself with:

```sh
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

Dependabot opens a pull request for a new version of a dependency each week.

## Keep the wallet small

Put in the wallet the money of 1 day of calls, and no more. A `budget` in the
configuration file limits 1 run of the payer. It does not limit the wallet.
