package payer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// accountRoute is the free route of the sidecar that gives the account.
	accountRoute = "/x402/account"
	// txRoute is the free route of the sidecar that gives the state of a
	// transaction.
	txRoute = "/x402/tx"
)

// ErrNoAccount says that the chain does not hold the account of an address.
//
// A chain makes an account when the address gets money for the first time. A
// new wallet therefore has no account, and it has no balance. That is not a
// fault: it is the normal state of a wallet that waits for its first TIA.
var ErrNoAccount = errors.New("the chain does not hold this account yet")

// AccountAnswer is the body of GET /x402/account of the sidecar.
//
// The numbers are strings, because an account number does not fit in the
// number type of every language.
type AccountAnswer struct {
	Address       string `json:"address"`
	Network       string `json:"network"`
	ChainID       string `json:"chainId"`
	AccountNumber string `json:"accountNumber"`
	Sequence      string `json:"sequence"`
	Denom         string `json:"denom"`
	// Balance is absent when the node did not answer the balance query.
	Balance string `json:"balance"`
}

// TxAnswer is the body of GET /x402/tx of the sidecar.
//
// The height is a string, because a block height does not fit in the number
// type of every language.
type TxAnswer struct {
	Hash    string `json:"hash"`
	Network string `json:"network"`
	Found   bool   `json:"found"`
	Height  string `json:"height"`
	Code    uint32 `json:"code"`
	Error   string `json:"error,omitempty"`
}

// Accounts reads the account of the payer from the sidecar.
//
// The payer needs the account number and the sequence before it signs, because
// the chain puts both in the signed bytes. The sidecar holds the connection to
// the node, so the payer needs none.
//
// NOTE: A hostile sidecar can give wrong numbers. It cannot take money that
// way: wrong numbers make the signature invalid, and the chain then rejects
// the payment. The price, the token and the recipient come from the checks of
// the payer, not from this answer.
type Accounts struct {
	upstream string
	network  string
	http     *http.Client
}

// NewAccounts builds a reader of accounts for a sidecar.
func NewAccounts(upstream, network string) *Accounts {
	return &Accounts{
		upstream: strings.TrimRight(upstream, "/"),
		network:  network,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Account returns the account number and the sequence of an address.
// It has the shape of an AccountFunc, so a Payer can use it.
func (a *Accounts) Account(ctx context.Context, address string) (AccountInfo, error) {
	answer, err := a.Read(ctx, address)
	if err != nil {
		return AccountInfo{}, err
	}

	number, err := strconv.ParseUint(answer.AccountNumber, 10, 64)
	if err != nil {
		return AccountInfo{}, fmt.Errorf(
			"the sidecar gave the account number %q, which is not a number", answer.AccountNumber)
	}
	sequence, err := strconv.ParseUint(answer.Sequence, 10, 64)
	if err != nil {
		return AccountInfo{}, fmt.Errorf(
			"the sidecar gave the sequence %q, which is not a number", answer.Sequence)
	}
	return AccountInfo{Number: number, Sequence: sequence}, nil
}

// TxStatus asks the sidecar what the chain knows about a transaction hash.
//
// It has the shape that Chain asks for, so a Payer can use it. The payer
// calls it when the answer to a payment did not arrive, and it must find out
// if the chain took the money.
//
// NOTE: A sidecar that gives a wrong answer here cannot take money. It can
// only make the payer count the budget wrong, or pick a sequence that the
// chain rejects. The chain, not the sidecar, decides what a signature buys.
func (a *Accounts) TxStatus(ctx context.Context, hash string) (TxStatus, error) {
	target := a.upstream + txRoute + "?" + url.Values{"hash": {hash}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return TxStatus{}, err
	}
	res, err := a.http.Do(req)
	if err != nil {
		return TxStatus{}, fmt.Errorf("the sidecar at %s did not answer: %w", a.upstream, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return TxStatus{}, fmt.Errorf("read the answer of the sidecar: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return TxStatus{}, a.statusError(txRoute, res.StatusCode, body)
	}

	var answer TxAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return TxStatus{}, fmt.Errorf(
			"the answer of %s%s is not readable: %w", a.upstream, txRoute, err)
	}

	status := TxStatus{Found: answer.Found, Code: answer.Code, Error: answer.Error}
	if answer.Height != "" {
		status.Height, _ = strconv.ParseInt(answer.Height, 10, 64)
	}
	return status, nil
}

// Read asks the sidecar for the complete account answer.
func (a *Accounts) Read(ctx context.Context, address string) (AccountAnswer, error) {
	target := a.upstream + accountRoute + "?" + url.Values{"address": {address}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return AccountAnswer{}, err
	}
	res, err := a.http.Do(req)
	if err != nil {
		return AccountAnswer{}, fmt.Errorf("the sidecar at %s did not answer: %w", a.upstream, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return AccountAnswer{}, fmt.Errorf("read the answer of the sidecar: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return AccountAnswer{}, a.statusError(accountRoute, res.StatusCode, body)
	}

	var answer AccountAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return AccountAnswer{}, fmt.Errorf(
			"the answer of %s%s is not readable: %w", a.upstream, accountRoute, err)
	}

	// The account number of another chain gives an invalid signature. Stop
	// here, where the reason is clear.
	if answer.Network != "" && answer.Network != a.network {
		return AccountAnswer{}, fmt.Errorf(
			"the sidecar is on %s, but the payer pays on %s", answer.Network, a.network)
	}
	if answer.AccountNumber == "" {
		return AccountAnswer{}, fmt.Errorf(
			"the sidecar gave no account number for %s", address)
	}
	return answer, nil
}

// noAccount reports if a message of the sidecar says that the chain holds no
// account for the address. The 2 forms come from the node and from the sidecar.
func noAccount(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "not found") || strings.Contains(m, "not on the chain")
}

// statusError turns an answer that is not 200 into a readable error.
//
// route is the route that the payer asked for. A 404 with no message means
// that the sidecar does not serve that route at all.
func (a *Accounts) statusError(route string, status int, body []byte) error {
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &out)

	switch status {
	case http.StatusNotFound:
		if out.Error != "" {
			// The sidecar answers 404 for every fault of its account
			// query, so the message says which fault it is. A chain that
			// does not hold the account is a normal state, and a caller
			// that only needs the balance can continue.
			if route == accountRoute && noAccount(out.Error) {
				return fmt.Errorf("%s: %w", out.Error, ErrNoAccount)
			}
			return fmt.Errorf("%s", out.Error)
		}
		// The route itself is absent, so the sidecar is an older build.
		return fmt.Errorf(
			"the sidecar at %s has no %s route. It must be a newer x402-celestia-rpc",
			a.upstream, route)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("the sidecar cannot read an account: %s", out.Error)
	default:
		if out.Error != "" {
			return fmt.Errorf("the sidecar answered %d: %s", status, out.Error)
		}
		return fmt.Errorf("the sidecar answered %d", status)
	}
}
