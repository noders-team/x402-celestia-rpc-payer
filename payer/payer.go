package payer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/noders-team/x402-celestia-rpc-payer/x402"
)

// AccountInfo holds the on-chain data that a payment signature needs.
type AccountInfo struct {
	// Number is the account number that the chain assigned.
	Number uint64
	// Sequence is the next sequence of the account.
	Sequence uint64
}

// TxStatus is what the chain knows about 1 transaction.
type TxStatus struct {
	// Found is true when the chain holds the transaction in a block.
	Found bool
	// Height is the block of the transaction. It is 0 when Found is false.
	Height int64
	// Code is the result code of the chain. 0 means that the payment moved.
	// Another value means that the chain took the fee and the account
	// sequence, and that the payment did not move.
	Code uint32
	// Error is the log of a transaction whose Code is not 0.
	Error string
}

// Chain is what the Payer must read before and after it signs.
//
// The payer has no node of its own, so the sidecar answers both calls, on its
// free routes /x402/account and /x402/tx. Tests replace it.
type Chain interface {
	// Account returns the account number and the sequence of an address.
	Account(ctx context.Context, address string) (AccountInfo, error)
	// TxStatus reports what the chain knows about a transaction hash.
	TxStatus(ctx context.Context, hash string) (TxStatus, error)
}

// Payment is 1 signed payment that the payer made.
type Payment struct {
	// Header is the value of the Payment-Signature header.
	Header string
	// Amount is the price that the payment pays.
	Amount *big.Int
	// Fee is the fee of the transaction. The payer keeps it here, so that
	// Rollback takes back the same sum that Pay counted.
	Fee *big.Int
	// Asset is the denom of the payment.
	Asset string
	// Sequence is the account sequence of the transaction.
	Sequence uint64
	// TxHash is the hash of the signed transaction. The payer computes it
	// from the bytes that it signed, so it knows the hash before the sidecar
	// broadcasts them. Unresolved uses it to ask the chain.
	TxHash string
}

const (
	// defaultResolveWait is how long Pay waits to learn if the chain took a
	// payment whose answer did not arrive. A Celestia block takes about
	// 6 s, so this covers 2 blocks.
	defaultResolveWait = 12 * time.Second
	// defaultResolveInterval is how often Pay asks the chain in that time.
	defaultResolveInterval = 2 * time.Second
)

// txOutcome is what the chain said about a transaction.
type txOutcome int

const (
	// txUnknown means that the chain did not answer. The payer cannot tell.
	txUnknown txOutcome = iota
	// txAbsent means that the chain answered, and that it holds no such
	// transaction.
	txAbsent
	// txInBlock means that the chain holds the transaction in a block.
	txInBlock
)

// Payer signs the payments.
//
// NOTE: The account sequence is part of the signed bytes, so 2 payments must
// not use the same sequence. The Payer holds a lock while it signs, and it
// keeps the next sequence in memory. It reads the sequence of the chain again
// after a payment fails.
type Payer struct {
	cfg      *Config
	wallet   *Wallet
	cdc      codec.Codec
	txConfig client.TxConfig

	// chain reads the account and the state of a transaction. Tests replace
	// it.
	chain Chain

	mu      sync.Mutex
	nextSeq uint64
	haveSeq bool
	spent   *big.Int
	count   int
	// open holds each payment whose answer did not reach the payer. The
	// Payer counts them against the budget, and it asks the chain about
	// them on the next payment.
	open []*Payment

	// resolveWait and resolveInterval time the question to the chain about
	// an open payment. Tests make them short.
	resolveWait     time.Duration
	resolveInterval time.Duration
}

// NewPayer builds a Payer for a wallet.
func NewPayer(cfg *Config, wallet *Wallet, chain Chain) *Payer {
	cdc := newCodec()
	return &Payer{
		cfg:             cfg,
		wallet:          wallet,
		cdc:             cdc,
		txConfig:        authtx.NewTxConfig(cdc, authtx.DefaultSignModes),
		chain:           chain,
		spent:           new(big.Int),
		resolveWait:     defaultResolveWait,
		resolveInterval: defaultResolveInterval,
	}
}

// newCodec builds a codec with the interfaces that a bank payment needs.
func newCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(registry)
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	vestingtypes.RegisterInterfaces(registry)
	banktypes.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry)
}

// Address returns the address of the payer.
func (p *Payer) Address() string { return p.wallet.Address() }

// Spent returns the money that left the wallet, and the number of payments.
// The sum holds the prices and the fees.
func (p *Payer) Spent() (*big.Int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return new(big.Int).Set(p.spent), p.count
}

// Pay signs a payment for a payment option of the sidecar.
//
// Pay does not broadcast the transaction. The sidecar broadcasts it after the
// sidecar verifies the payment.
func (p *Payer) Pay(ctx context.Context, option x402.PaymentOption) (*Payment, error) {
	amount, err := p.checkOption(option)
	if err != nil {
		return nil, err
	}

	// Each payment burns the price and the fee. The budget covers both, and
	// the transaction and the budget use this same number.
	fee, err := p.feeAmount()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Ask the chain about each payment whose answer did not arrive, before
	// this payment counts against the budget and picks a sequence.
	p.resolveOpen(ctx)

	if err := p.checkBudget(amount, fee); err != nil {
		return nil, err
	}

	account, err := p.chain.Account(ctx, p.wallet.Address())
	if err != nil {
		return nil, fmt.Errorf("read the account of %s: %w", p.wallet.Address(), err)
	}

	sequence := account.Sequence
	if p.haveSeq && p.nextSeq > sequence {
		sequence = p.nextSeq
	}

	timeoutAt := time.Now().Add(time.Duration(p.cfg.TimeoutSeconds) * time.Second).Unix()

	signedTx, txHash, err := p.signTx(ctx, option, amount, fee, account.Number, sequence)
	if err != nil {
		return nil, err
	}

	header, err := x402.EncodeHeader(x402.PaymentPayloadV2{
		X402Version: x402.VersionV2,
		Accepted:    option,
		Payload: mustJSON(x402.CosmosPayload{
			Authorization: x402.CosmosAuthorization{
				From:      p.wallet.Address(),
				To:        option.PayTo,
				Amount:    amount.String(),
				Denom:     option.Asset,
				TimeoutAt: timeoutAt,
			},
			SignedTx: signedTx,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("encode the payment header: %w", err)
	}

	p.nextSeq = sequence + 1
	p.haveSeq = true
	p.spent.Add(p.spent, amount)
	p.spent.Add(p.spent, fee)
	p.count++

	return &Payment{
		Header:   header,
		Amount:   amount,
		Fee:      fee,
		Asset:    option.Asset,
		Sequence: sequence,
		TxHash:   txHash,
	}, nil
}

// Rollback takes a payment back.
//
// Call it when the sidecar answered and refused the payment. The sidecar
// broadcast nothing in that case, so the money is still in the wallet. The
// Payer reads the sequence of the chain again on the next call.
//
// CAUTION: Do not call Rollback when the answer of the sidecar did not
// arrive. The sidecar may have broadcast the transaction. Call Unresolved.
func (p *Payer) Rollback(pay *Payment) {
	if pay == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release(pay)
}

// Unresolved records a payment whose answer did not reach the payer.
//
// The sidecar may have broadcast the transaction before the answer went
// missing, so the money may be gone. The Payer therefore keeps the price, the
// fee and the account sequence, and it asks the chain on the next payment.
//
// That order is the safe one. It counts money that may still be in the
// wallet, so the budget stops early and never late. And it never signs a
// sequence that the chain already holds.
func (p *Payer) Unresolved(pay *Payment) {
	if pay == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pay.TxHash == "" {
		// Without a hash the Payer cannot ask the chain, so it gives the
		// payment back.
		p.release(pay)
		return
	}
	p.open = append(p.open, pay)
}

// Open returns the hash of each payment whose answer did not arrive, and
// which the Payer has not resolved yet.
func (p *Payer) Open() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	hashes := make([]string, 0, len(p.open))
	for _, pay := range p.open {
		hashes = append(hashes, pay.TxHash)
	}
	return hashes
}

// release gives a payment back: the price, the fee, the count, and the
// account sequence. The caller holds the lock.
func (p *Payer) release(pay *Payment) {
	p.haveSeq = false
	// Take back the price and the fee, which is what Pay counted.
	if pay.Amount != nil {
		p.spent.Sub(p.spent, pay.Amount)
	}
	if pay.Fee != nil {
		p.spent.Sub(p.spent, pay.Fee)
	}
	p.count--
}

// resolveOpen asks the chain about each payment whose answer did not arrive.
//
// A payment that reached a block stands: the money left the wallet, and the
// chain holds the account sequence. A payment that the chain does not hold
// goes back. A chain that does not answer leaves the payment open, and the
// Payer asks again on the next payment.
//
// The caller holds the lock.
func (p *Payer) resolveOpen(ctx context.Context) {
	if len(p.open) == 0 {
		return
	}

	keep := p.open[:0]
	for _, pay := range p.open {
		outcome, status := p.waitForTx(ctx, pay.TxHash)
		switch outcome {
		case txUnknown:
			keep = append(keep, pay)
		case txAbsent:
			p.release(pay)
		case txInBlock:
			if status.Code != 0 {
				// The chain took the fee and the account sequence. The
				// payment did not move, so give the price back. The
				// sequence stays, because the chain holds it.
				if pay.Amount != nil {
					p.spent.Sub(p.spent, pay.Amount)
				}
			}
		}
	}
	p.open = keep
}

// waitForTx asks the chain about a hash until the chain holds the transaction
// in a block, or until the time limit ends.
//
// A transaction needs a block, so the first answer is often "not there". This
// function therefore waits for about 2 blocks before it reports txAbsent.
func (p *Payer) waitForTx(ctx context.Context, hash string) (txOutcome, TxStatus) {
	deadline := time.Now().Add(p.resolveWait)
	outcome := txUnknown

	for {
		status, err := p.chain.TxStatus(ctx, hash)
		if err == nil {
			if status.Found {
				return txInBlock, status
			}
			// The chain answered, and it holds no such transaction. That
			// answer counts only after the time limit, because a
			// transaction that waits for a block gives the same answer.
			outcome = txAbsent
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return outcome, TxStatus{}
		}
		select {
		case <-ctx.Done():
			return outcome, TxStatus{}
		case <-time.After(p.resolveInterval):
		}
	}
}

// checkOption checks the payment option of the sidecar against the rules of
// the configuration. It returns the price of the call.
func (p *Payer) checkOption(option x402.PaymentOption) (*big.Int, error) {
	if option.Network != p.cfg.Network {
		return nil, fmt.Errorf("the sidecar asks for %q, but the payer pays on %q",
			option.Network, p.cfg.Network)
	}
	if option.Asset != p.cfg.Asset {
		return nil, fmt.Errorf("the sidecar asks for the token %q, but the payer pays %q",
			option.Asset, p.cfg.Asset)
	}
	if option.Scheme != x402.SchemeExact && option.Scheme != x402.SchemeUpto {
		return nil, fmt.Errorf("the scheme %q is not known", option.Scheme)
	}
	if !strings.HasPrefix(option.PayTo, Bech32Prefix+"1") {
		return nil, fmt.Errorf("the address %q is not a %s address", option.PayTo, Bech32Prefix)
	}

	amount, ok := new(big.Int).SetString(strings.TrimSpace(option.Amount), 10)
	if !ok {
		return nil, fmt.Errorf("the price %q is not a whole number", option.Amount)
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("the price %q is not above 0", option.Amount)
	}
	if limit := p.cfg.MaxPerCallAmount(); amount.Cmp(limit) > 0 {
		return nil, fmt.Errorf("the price %s %s is above maxPerCall %s",
			amount, option.Asset, limit)
	}
	return amount, nil
}

// checkBudget makes sure that the payment stays inside the budget.
//
// The budget covers the price and the fee, because both leave the wallet.
// The caller holds the lock.
func (p *Payer) checkBudget(amount, fee *big.Int) error {
	budget := p.cfg.BudgetAmount()
	if budget.Sign() == 0 {
		return nil
	}
	after := new(big.Int).Add(p.spent, amount)
	after.Add(after, fee)
	if after.Cmp(budget) > 0 {
		return fmt.Errorf("the payment of %s %s and the fee of %s %s put the sum at %s, above budget %s",
			amount, p.cfg.Asset, fee, p.cfg.Asset, after, budget)
	}
	return nil
}

// feeAmount returns the fee of 1 payment, in the atomic unit of the asset.
func (p *Payer) feeAmount() (*big.Int, error) {
	fee, ok := new(big.Int).SetString(strings.TrimSpace(p.cfg.Fee), 10)
	if !ok {
		return nil, fmt.Errorf("fee %q is not a whole number", p.cfg.Fee)
	}
	if fee.Sign() < 0 {
		return nil, fmt.Errorf("fee %q is below 0", p.cfg.Fee)
	}
	return fee, nil
}

// signTx builds a bank MsgSend and signs it with SIGN_MODE_DIRECT.
//
// It returns the base64 of the signed TxRaw, and the hash of the same bytes.
// A Cosmos chain names a transaction by the SHA-256 of those bytes, so the
// payer knows the hash before the sidecar broadcasts the transaction.
func (p *Payer) signTx(ctx context.Context, option x402.PaymentOption, amount, fee *big.Int, accountNumber, sequence uint64) (signedTx, txHash string, err error) {
	coin := sdk.NewCoin(option.Asset, math.NewIntFromBigInt(amount))

	builder := p.txConfig.NewTxBuilder()
	msg := &banktypes.MsgSend{
		FromAddress: p.wallet.Address(),
		ToAddress:   option.PayTo,
		// The verifier of the sidecar accepts exactly 1 coin.
		Amount: sdk.Coins{coin},
	}
	if err := builder.SetMsgs(msg); err != nil {
		return "", "", fmt.Errorf("set the message: %w", err)
	}
	builder.SetGasLimit(p.cfg.GasLimit)
	builder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin(p.cfg.Asset, math.NewIntFromBigInt(fee))))
	builder.SetMemo(p.cfg.Memo)

	mode := signingtypes.SignMode_SIGN_MODE_DIRECT
	pubKey := p.wallet.PubKey()

	// A first pass with an empty signature fixes the AuthInfo bytes.
	blank := signingtypes.SignatureV2{
		PubKey:   pubKey,
		Data:     &signingtypes.SingleSignatureData{SignMode: mode},
		Sequence: sequence,
	}
	if err := builder.SetSignatures(blank); err != nil {
		return "", "", fmt.Errorf("set the empty signature: %w", err)
	}

	// GetSignBytesAdapter builds the same bytes that the ante handler of the
	// chain checks. The chain-id and the account number are part of them.
	signerData := authsigning.SignerData{
		Address:       p.wallet.Address(),
		ChainID:       p.cfg.ChainID(),
		AccountNumber: accountNumber,
		Sequence:      sequence,
		PubKey:        pubKey,
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		ctx, p.txConfig.SignModeHandler(), mode, signerData, builder.GetTx())
	if err != nil {
		return "", "", fmt.Errorf("build the sign bytes: %w", err)
	}

	signature, err := p.wallet.Sign(signBytes)
	if err != nil {
		return "", "", fmt.Errorf("sign the payment: %w", err)
	}

	signed := signingtypes.SignatureV2{
		PubKey:   pubKey,
		Data:     &signingtypes.SingleSignatureData{SignMode: mode, Signature: signature},
		Sequence: sequence,
	}
	if err := builder.SetSignatures(signed); err != nil {
		return "", "", fmt.Errorf("set the signature: %w", err)
	}

	txBytes, err := p.txConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		return "", "", fmt.Errorf("encode the transaction: %w", err)
	}
	sum := sha256.Sum256(txBytes)
	return base64.StdEncoding.EncodeToString(txBytes),
		strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

// mustJSON encodes a value that cannot fail to encode.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
