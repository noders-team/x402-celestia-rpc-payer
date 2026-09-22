// Package payer pays for the calls of an x402 Celestia RPC sidecar.
//
// The package has 6 parts:
//
//   - Config reads and checks the YAML configuration file.
//   - Wallet derives the key of a BIP-39 mnemonic and signs with it.
//   - Accounts reads what the payer needs from the sidecar: the account
//     number and the sequence, on the free /x402/account route, and the state
//     of a transaction, on the free /x402/tx route. It is the Chain of the
//     Payer.
//   - Payer signs 1 bank MsgSend for 1 payment option. It does not broadcast
//     the transaction: the sidecar broadcasts it after it verifies it.
//   - Client sends a request to the sidecar. When the sidecar answers 402, the
//     Client pays and sends the request 1 more time.
//   - Proxy is a local CometBFT RPC endpoint in front of the Client.
//
// A program builds them in this order:
//
//	cfg, err := payer.LoadConfig("config.yaml")
//	wallet, err := payer.WalletFromConfig(cfg)
//	accounts := payer.NewAccounts(cfg.Upstream, cfg.Network)
//	p := payer.NewPayer(cfg, wallet, accounts)
//	client := payer.NewClient(cfg, p, log)
//	http.ListenAndServe(cfg.Listen, payer.NewProxy(cfg, client, log))
//
// WARNING: The private key of the wallet stays in the memory of the process
// while it runs. Run the payer on a machine that you control.
package payer
