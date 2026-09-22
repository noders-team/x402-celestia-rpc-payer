package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"time"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

const (
	// fundInterval is how often fund reads the balance.
	fundInterval = 5 * time.Second
	// fundLimit is how long fund waits before it gives up.
	fundLimit = 30 * time.Minute
)

// balanceReader reads the balance of the payer wallet, and the denom of that
// balance. Tests replace it.
type balanceReader func(ctx context.Context) (balance, denom string, err error)

// funder shows the address of the payer and waits for the money.
type funder struct {
	cfg     *payer.Config
	address string
	read    balanceReader
	out     io.Writer

	interval time.Duration
	limit    time.Duration
}

// newFunder builds a funder that reads the balance through the sidecar.
func newFunder(cfg *payer.Config, address string, out io.Writer) *funder {
	accounts := payer.NewAccounts(cfg.Upstream, cfg.Network)
	return &funder{
		cfg:     cfg,
		address: address,
		out:     out,
		read: func(ctx context.Context) (string, string, error) {
			answer, err := accounts.Read(ctx, address)
			if errors.Is(err, payer.ErrNoAccount) {
				// The chain makes an account when the address gets money
				// for the first time. A new wallet has none, and a new
				// wallet is what fund exists for. It holds 0, so fund
				// waits.
				return "0", cfg.Asset, nil
			}
			if err != nil {
				return "", "", err
			}
			// An empty balance means that the node did not answer the
			// balance query. It does not mean 0. The money may be there
			// already, so the payer must not wait for it.
			if answer.Balance == "" {
				return "", "", fmt.Errorf("the sidecar at %s did not give the balance of %s",
					cfg.Upstream, address)
			}
			return answer.Balance, answer.Denom, nil
		},
		interval: fundInterval,
		limit:    fundLimit,
	}
}

// run shows the address, then it waits until the wallet holds money.
//
// NOTE: The message names no faucet and no web page. A test network needs a
// faucet, and the mainnet needs an exchange or another wallet. 1 message must
// be correct for both.
func (f *funder) run(ctx context.Context) error {
	balance, denom, err := f.read(ctx)
	if err != nil {
		return err
	}
	if denom == "" {
		denom = f.cfg.Asset
	}

	fmt.Fprintf(f.out, "Your address\n  %s\n\n", f.address)
	fmt.Fprintf(f.out, "Network  %s\n", f.cfg.Network)
	fmt.Fprintf(f.out, "Balance  %s %s\n\n", balance, denom)
	fmt.Fprintf(f.out, "Send TIA to this address. A call costs up to %s %s, plus a %s %s fee.\n",
		f.cfg.MaxPerCall, denom, f.cfg.Fee, denom)

	if isAbove0(balance) {
		f.report(balance, denom)
		return nil
	}

	fmt.Fprint(f.out, "\nThe payer waits for the money. Stop it with Ctrl-C.\n")

	ctx, cancel := context.WithTimeout(ctx, f.limit)
	defer cancel()

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return f.timeoutError()
		case <-ticker.C:
		}

		balance, next, err := f.read(ctx)
		if err != nil {
			// The time limit can end a read that is on its way. The
			// customer must then read the message of the limit, and not
			// the message of a request that somebody cancelled.
			if ctx.Err() != nil {
				return f.timeoutError()
			}
			return err
		}
		if next != "" {
			denom = next
		}
		if isAbove0(balance) {
			f.report(balance, denom)
			return nil
		}
		fmt.Fprint(f.out, ".")
	}
}

// timeoutError says that the money did not arrive inside the time limit.
func (f *funder) timeoutError() error {
	return fmt.Errorf("the money did not arrive in %s. Run the command again", f.limit)
}

// report prints the balance and what it buys.
func (f *funder) report(balance, denom string) {
	calls := callsFor(balance, f.cfg.MaxPerCall, f.cfg.Fee)
	word := "calls"
	if calls == 1 {
		word = "call"
	}
	fmt.Fprintf(f.out, "\nBalance %s %s. That pays for about %d %s.\n",
		balance, denom, calls, word)
}

// isAbove0 reports if a balance holds money.
func isAbove0(balance string) bool {
	n, ok := new(big.Int).SetString(balance, 10)
	return ok && n.Sign() > 0
}

// callsFor returns the number of calls that a balance pays for. Each call
// costs the highest price plus the fee.
func callsFor(balance, maxPerCall, fee string) int64 {
	have, ok := new(big.Int).SetString(balance, 10)
	if !ok {
		return 0
	}
	price, ok := new(big.Int).SetString(maxPerCall, 10)
	if !ok {
		return 0
	}
	cost, ok := new(big.Int).SetString(fee, 10)
	if !ok {
		return 0
	}
	cost.Add(cost, price)
	if cost.Sign() <= 0 {
		return 0
	}
	return new(big.Int).Div(have, cost).Int64()
}

// runFund is the fund command.
func runFund(cfg *payer.Config) error {
	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		return err
	}
	return newFunder(cfg, wallet.Address(), os.Stdout).run(context.Background())
}
