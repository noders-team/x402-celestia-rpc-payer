package payer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	txn "github.com/cosmos/cosmos-sdk/types/tx"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/noders-team/x402-celestia-rpc-payer/x402"
)

// testPayTo is a real Celestia address. It is the test mnemonic at index 9.
const testPayTo = "celestia1kpr42yd4753cymc24ps78cm8dqf203qp0zwlgt"

// fakeChain is an AccountFunc that answers from memory.
type fakeChain struct {
	number   uint64
	sequence uint64
	err      error
	calls    int
}

func (f *fakeChain) Account(context.Context, string) (AccountInfo, error) {
	f.calls++
	if f.err != nil {
		return AccountInfo{}, f.err
	}
	return AccountInfo{Number: f.number, Sequence: f.sequence}, nil
}

// newTestPayer builds a payer with a fake chain.
func newTestPayer(t *testing.T, body string) (*Payer, *fakeChain) {
	t.Helper()

	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	wallet, err := WalletFromConfig(cfg)
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	chain := &fakeChain{number: 42, sequence: 7}
	return NewPayer(cfg, wallet, chain.Account), chain
}

// testOption is the payment option that a sidecar publishes.
func testOption(amount string) x402.PaymentOption {
	return x402.PaymentOption{
		Scheme:            x402.SchemeExact,
		Network:           "cosmos:mocha-5",
		Amount:            amount,
		Asset:             "utia",
		PayTo:             testPayTo,
		MaxTimeoutSeconds: 120,
	}
}

// decodePayment reads the header that the payer made.
func decodePayment(t *testing.T, header string) (x402.PaymentPayloadV2, x402.CosmosPayload) {
	t.Helper()

	payload, err := x402.ParsePaymentPayload(header)
	if err != nil {
		t.Fatalf("parse the payment header: %v", err)
	}
	var cosmosPayload x402.CosmosPayload
	if err := json.Unmarshal(payload.Payload, &cosmosPayload); err != nil {
		t.Fatalf("parse the cosmos payload: %v", err)
	}
	return payload, cosmosPayload
}

// checkSignedTx runs the checks of the verifier of the sidecar on a signed
// transaction. It rebuilds the sign bytes with GetSignBytesAdapter, which is
// the helper that the ante handler of the chain uses.
func checkSignedTx(t *testing.T, signedTx, chainID, from string, accountNumber uint64) *banktypes.MsgSend {
	t.Helper()

	txRawBytes, err := base64.StdEncoding.DecodeString(signedTx)
	if err != nil {
		t.Fatalf("decode the signed tx: %v", err)
	}

	cdc := newCodec()
	txConfig := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	decoded, err := txConfig.TxDecoder()(txRawBytes)
	if err != nil {
		t.Fatalf("decode the tx: %v", err)
	}

	msgs := decoded.GetMsgs()
	if len(msgs) != 1 {
		t.Fatalf("the tx has %d messages, want 1", len(msgs))
	}
	msgSend, ok := msgs[0].(*banktypes.MsgSend)
	if !ok {
		t.Fatalf("the message is a %T, want a MsgSend", msgs[0])
	}

	var raw txn.TxRaw
	if err := cdc.Unmarshal(txRawBytes, &raw); err != nil {
		t.Fatalf("decode the TxRaw: %v", err)
	}
	if len(raw.Signatures) != 1 {
		t.Fatalf("the tx has %d signatures, want 1", len(raw.Signatures))
	}

	var authInfo txn.AuthInfo
	if err := cdc.Unmarshal(raw.AuthInfoBytes, &authInfo); err != nil {
		t.Fatalf("decode the AuthInfo: %v", err)
	}
	if len(authInfo.SignerInfos) != 1 {
		t.Fatalf("the tx has %d signers, want 1", len(authInfo.SignerInfos))
	}
	signerInfo := authInfo.SignerInfos[0]
	single := signerInfo.ModeInfo.GetSingle()
	if single == nil {
		t.Fatalf("the tx does not use a single signature")
	}

	var pubKey cryptotypes.PubKey
	if err := cdc.UnpackAny(signerInfo.PublicKey, &pubKey); err != nil {
		t.Fatalf("unpack the public key: %v", err)
	}

	signerData := authsigning.SignerData{
		Address:       from,
		ChainID:       chainID,
		AccountNumber: accountNumber,
		Sequence:      signerInfo.Sequence,
		PubKey:        pubKey,
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), txConfig.SignModeHandler(), single.Mode, signerData, decoded)
	if err != nil {
		t.Fatalf("build the sign bytes: %v", err)
	}
	if !pubKey.VerifySignature(signBytes, raw.Signatures[0]) {
		t.Fatalf("the signature is not valid for the chain-id %q", chainID)
	}
	return msgSend
}

func TestPayerSignsAPayment(t *testing.T) {
	payer, chain := newTestPayer(t, minimalConfig)

	pay, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if pay.Amount.String() != "1000" {
		t.Errorf("amount = %s, want 1000", pay.Amount)
	}
	if pay.Sequence != chain.sequence {
		t.Errorf("sequence = %d, want %d", pay.Sequence, chain.sequence)
	}

	payload, cosmosPayload := decodePayment(t, pay.Header)
	if payload.Accepted.Network != "cosmos:mocha-5" {
		t.Errorf("network = %q", payload.Accepted.Network)
	}
	if cosmosPayload.Authorization.From != testAddress {
		t.Errorf("from = %q, want %q", cosmosPayload.Authorization.From, testAddress)
	}
	if cosmosPayload.Authorization.To != testPayTo {
		t.Errorf("to = %q, want %q", cosmosPayload.Authorization.To, testPayTo)
	}
	if cosmosPayload.Authorization.Amount != "1000" {
		t.Errorf("amount = %q, want \"1000\"", cosmosPayload.Authorization.Amount)
	}
	if cosmosPayload.Authorization.Denom != "utia" {
		t.Errorf("denom = %q, want \"utia\"", cosmosPayload.Authorization.Denom)
	}
	if cosmosPayload.Authorization.TimeoutAt <= 0 {
		t.Errorf("timeoutAt = %d, want a time in the future", cosmosPayload.Authorization.TimeoutAt)
	}

	msgSend := checkSignedTx(t, cosmosPayload.SignedTx, "mocha-5", testAddress, chain.number)
	if msgSend.FromAddress != testAddress {
		t.Errorf("the tx sender is %q, want %q", msgSend.FromAddress, testAddress)
	}
	if msgSend.ToAddress != testPayTo {
		t.Errorf("the tx recipient is %q, want %q", msgSend.ToAddress, testPayTo)
	}
	if len(msgSend.Amount) != 1 {
		t.Fatalf("the tx sends %d coins, want 1", len(msgSend.Amount))
	}
	if msgSend.Amount[0].Denom != "utia" || msgSend.Amount[0].Amount.String() != "1000" {
		t.Errorf("the tx sends %s, want 1000utia", msgSend.Amount[0])
	}
}

func TestPayerSignsForTheChainOfTheNetwork(t *testing.T) {
	payer, chain := newTestPayer(t, minimalConfig)

	pay, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	_, cosmosPayload := decodePayment(t, pay.Header)

	// The signature holds for mocha-5. It must not hold for another chain.
	checkSignedTx(t, cosmosPayload.SignedTx, "mocha-5", testAddress, chain.number)

	txRawBytes, err := base64.StdEncoding.DecodeString(cosmosPayload.SignedTx)
	if err != nil {
		t.Fatalf("decode the signed tx: %v", err)
	}
	cdc := newCodec()
	txConfig := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	decoded, err := txConfig.TxDecoder()(txRawBytes)
	if err != nil {
		t.Fatalf("decode the tx: %v", err)
	}
	var raw txn.TxRaw
	if err := cdc.Unmarshal(txRawBytes, &raw); err != nil {
		t.Fatalf("decode the TxRaw: %v", err)
	}

	wallet, err := NewWallet(testMnemonic, "m/44'/118'/0'/0/0", "celestia")
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	signerData := authsigning.SignerData{
		Address:       testAddress,
		ChainID:       "celestia",
		AccountNumber: chain.number,
		Sequence:      chain.sequence,
		PubKey:        wallet.PubKey(),
	}
	otherBytes, err := authsigning.GetSignBytesAdapter(context.Background(),
		txConfig.SignModeHandler(), 1, signerData, decoded)
	if err != nil {
		t.Fatalf("build the sign bytes: %v", err)
	}
	if wallet.PubKey().VerifySignature(otherBytes, raw.Signatures[0]) {
		t.Errorf("the signature holds for the chain-id \"celestia\", but it must not")
	}
}

func TestPayerRejects(t *testing.T) {
	tests := []struct {
		name   string
		option x402.PaymentOption
		reason string
	}{
		{
			name: "another network",
			option: func() x402.PaymentOption {
				o := testOption("1000")
				o.Network = "cosmos:celestia"
				return o
			}(),
			reason: "but the payer pays on",
		},
		{
			name: "another token",
			option: func() x402.PaymentOption {
				o := testOption("1000")
				o.Asset = "ibc/ABCD"
				return o
			}(),
			reason: "but the payer pays",
		},
		{
			name: "a scheme that is not known",
			option: func() x402.PaymentOption {
				o := testOption("1000")
				o.Scheme = "free"
				return o
			}(),
			reason: "is not known",
		},
		{
			name: "an address of another chain",
			option: func() x402.PaymentOption {
				o := testOption("1000")
				o.PayTo = "cosmos1kpr42yd4753cymc24ps78cm8dqf203qp7gl0jx"
				return o
			}(),
			reason: "is not a celestia address",
		},
		{
			name:   "a price that is not a number",
			option: testOption("many"),
			reason: "not a whole number",
		},
		{
			name:   "a price of 0",
			option: testOption("0"),
			reason: "not above 0",
		},
		{
			name:   "a price above the limit",
			option: testOption("20001"),
			reason: "above maxPerCall",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payer, _ := newTestPayer(t, minimalConfig)
			_, err := payer.Pay(context.Background(), tt.option)
			if err == nil {
				t.Fatalf("the payment was made, but it must fail")
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error = %q, want it to hold %q", err, tt.reason)
			}
		})
	}
}

func TestPayerKeepsTheBudget(t *testing.T) {
	// 1 payment costs the price of 1000 and the fee of 2000, so 3000. The
	// budget of 6500 pays for 2 of them, and it stops the third.
	payer, _ := newTestPayer(t, minimalConfig+"budget: \"6500\"\n")

	for i := 0; i < 2; i++ {
		if _, err := payer.Pay(context.Background(), testOption("1000")); err != nil {
			t.Fatalf("payment %d: %v", i+1, err)
		}
	}

	_, err := payer.Pay(context.Background(), testOption("1000"))
	if err == nil {
		t.Fatalf("the third payment was made, but the budget is 6500")
	}
	if !strings.Contains(err.Error(), "above budget") {
		t.Errorf("error = %q, want it to hold \"above budget\"", err)
	}

	spent, count := payer.Spent()
	if spent.String() != "6000" {
		t.Errorf("spent = %s, want 6000", spent)
	}
	if count != 2 {
		t.Errorf("payments = %d, want 2", count)
	}
}

func TestPayerCountsTheFee(t *testing.T) {
	payer, _ := newTestPayer(t, minimalConfig)

	pay, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if pay.Fee == nil || pay.Fee.String() != "2000" {
		t.Fatalf("fee = %v, want 2000", pay.Fee)
	}

	// Spent holds the money that left the wallet: the price and the fee.
	spent, _ := payer.Spent()
	if spent.String() != "3000" {
		t.Errorf("spent = %s, want 3000", spent)
	}

	// Rollback takes back the same sum that Pay counted.
	payer.Rollback(pay)
	spent, count := payer.Spent()
	if spent.Sign() != 0 || count != 0 {
		t.Errorf("spent = %s, payments = %d, want 0 and 0", spent, count)
	}
}

func TestPayerRefusesAPaymentWhoseFeeIsAboveTheBudget(t *testing.T) {
	// The price of 1000 is inside the budget of 2500, but the price and the
	// fee of 2000 are not.
	payer, _ := newTestPayer(t, minimalConfig+"budget: \"2500\"\n")

	_, err := payer.Pay(context.Background(), testOption("1000"))
	if err == nil {
		t.Fatalf("the payment was made, but the price and the fee are 3000")
	}
	if !strings.Contains(err.Error(), "above budget") {
		t.Errorf("error = %q, want it to hold \"above budget\"", err)
	}
	if !strings.Contains(err.Error(), "fee") {
		t.Errorf("error = %q, want it to name the fee", err)
	}
}

func TestPayerCountsTheSequenceUp(t *testing.T) {
	payer, chain := newTestPayer(t, minimalConfig)

	first, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("first payment: %v", err)
	}
	// The chain does not know the first payment yet.
	second, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("second payment: %v", err)
	}

	if first.Sequence != chain.sequence {
		t.Errorf("the first sequence is %d, want %d", first.Sequence, chain.sequence)
	}
	if second.Sequence != first.Sequence+1 {
		t.Errorf("the second sequence is %d, want %d", second.Sequence, first.Sequence+1)
	}
}

func TestPayerTakesTheChainSequenceWhenItIsHigher(t *testing.T) {
	payer, chain := newTestPayer(t, minimalConfig)

	if _, err := payer.Pay(context.Background(), testOption("1000")); err != nil {
		t.Fatalf("first payment: %v", err)
	}
	chain.sequence = 20

	pay, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("second payment: %v", err)
	}
	if pay.Sequence != 20 {
		t.Errorf("sequence = %d, want 20", pay.Sequence)
	}
}

func TestPayerRollbackGivesTheMoneyBack(t *testing.T) {
	payer, chain := newTestPayer(t, minimalConfig)

	pay, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	payer.Rollback(pay)

	spent, count := payer.Spent()
	if spent.Sign() != 0 {
		t.Errorf("spent = %s, want 0", spent)
	}
	if count != 0 {
		t.Errorf("payments = %d, want 0", count)
	}

	// After a rollback the payer reads the sequence of the chain again.
	next, err := payer.Pay(context.Background(), testOption("1000"))
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if next.Sequence != chain.sequence {
		t.Errorf("sequence = %d, want %d", next.Sequence, chain.sequence)
	}
}

func TestPayerReportsAChainError(t *testing.T) {
	payer, chain := newTestPayer(t, minimalConfig)
	chain.err = context.DeadlineExceeded

	_, err := payer.Pay(context.Background(), testOption("1000"))
	if err == nil {
		t.Fatalf("the payment was made, but the chain gives an error")
	}
	if !strings.Contains(err.Error(), "read the account") {
		t.Errorf("error = %q, want it to hold \"read the account\"", err)
	}
}
