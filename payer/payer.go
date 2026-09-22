package payer

import (
	"context"
	"encoding/base64"
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

// AccountFunc reads the account of an address from a chain.
type AccountFunc func(ctx context.Context, address string) (AccountInfo, error)

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
	// TxHash is empty. The sidecar broadcasts the transaction, so only the
	// sidecar knows the hash.
	TxHash string
}

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

	// account reads the account of the payer. Tests replace it.
	account AccountFunc

	mu      sync.Mutex
	nextSeq uint64
	haveSeq bool
	spent   *big.Int
	count   int
}

// NewPayer builds a Payer for a wallet.
func NewPayer(cfg *Config, wallet *Wallet, account AccountFunc) *Payer {
	cdc := newCodec()
	return &Payer{
		cfg:      cfg,
		wallet:   wallet,
		cdc:      cdc,
		txConfig: authtx.NewTxConfig(cdc, authtx.DefaultSignModes),
		account:  account,
		spent:    new(big.Int),
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

	if err := p.checkBudget(amount, fee); err != nil {
		return nil, err
	}

	account, err := p.account(ctx, p.wallet.Address())
	if err != nil {
		return nil, fmt.Errorf("read the account of %s: %w", p.wallet.Address(), err)
	}

	sequence := account.Sequence
	if p.haveSeq && p.nextSeq > sequence {
		sequence = p.nextSeq
	}

	timeoutAt := time.Now().Add(time.Duration(p.cfg.TimeoutSeconds) * time.Second).Unix()

	signedTx, err := p.signTx(ctx, option, amount, fee, account.Number, sequence)
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
	}, nil
}

// Rollback takes a payment back. Call it when the sidecar did not accept the
// payment. The Payer reads the sequence of the chain again on the next call.
func (p *Payer) Rollback(pay *Payment) {
	if pay == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
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
// It returns the base64 of the signed TxRaw.
func (p *Payer) signTx(ctx context.Context, option x402.PaymentOption, amount, fee *big.Int, accountNumber, sequence uint64) (string, error) {
	coin := sdk.NewCoin(option.Asset, math.NewIntFromBigInt(amount))

	builder := p.txConfig.NewTxBuilder()
	msg := &banktypes.MsgSend{
		FromAddress: p.wallet.Address(),
		ToAddress:   option.PayTo,
		// The verifier of the sidecar accepts exactly 1 coin.
		Amount: sdk.Coins{coin},
	}
	if err := builder.SetMsgs(msg); err != nil {
		return "", fmt.Errorf("set the message: %w", err)
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
		return "", fmt.Errorf("set the empty signature: %w", err)
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
		return "", fmt.Errorf("build the sign bytes: %w", err)
	}

	signature, err := p.wallet.Sign(signBytes)
	if err != nil {
		return "", fmt.Errorf("sign the payment: %w", err)
	}

	signed := signingtypes.SignatureV2{
		PubKey:   pubKey,
		Data:     &signingtypes.SingleSignatureData{SignMode: mode, Signature: signature},
		Sequence: sequence,
	}
	if err := builder.SetSignatures(signed); err != nil {
		return "", fmt.Errorf("set the signature: %w", err)
	}

	txBytes, err := p.txConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		return "", fmt.Errorf("encode the transaction: %w", err)
	}
	return base64.StdEncoding.EncodeToString(txBytes), nil
}

// mustJSON encodes a value that cannot fail to encode.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
