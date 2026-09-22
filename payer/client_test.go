package payer

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getFrom builds a request for a URI-style call.
func getFrom(cfg *Config, path string) RequestFunc {
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, strings.TrimRight(cfg.Upstream, "/")+path, nil)
	}
}

func TestClientDoesNotPayForAFreeCall(t *testing.T) {
	client, sidecar, cfg := newTestClient(t, &fakeSidecar{}, "")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/status"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if receipt.Paid {
		t.Errorf("the payer paid for a free call")
	}
	if body := readBody(t, res); body["result"] != "free" {
		t.Errorf("body = %v", body)
	}
	if requests, payments := sidecar.counts(); requests != 1 || payments != 0 {
		t.Errorf("requests = %d, payments = %d, want 1 and 0", requests, payments)
	}
}

func TestClientPaysForACall(t *testing.T) {
	client, sidecar, cfg := newTestClient(t, &fakeSidecar{price: "1000"}, "")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !receipt.Paid {
		t.Fatalf("the payer did not pay")
	}
	if receipt.Amount != "1000" || receipt.Asset != "utia" {
		t.Errorf("receipt = %s %s, want 1000 utia", receipt.Amount, receipt.Asset)
	}
	if receipt.Response == nil || receipt.Response.TransactionHash != "ABCD1234" {
		t.Errorf("the receipt has no transaction hash: %+v", receipt.Response)
	}
	if body := readBody(t, res); body["result"] != "paid" {
		t.Errorf("body = %v", body)
	}

	if requests, payments := sidecar.counts(); requests != 2 || payments != 1 {
		t.Errorf("requests = %d, payments = %d, want 2 and 1", requests, payments)
	}

	payload, cosmosPayload := sidecar.lastPayment(t)
	if payload.Accepted.Amount != "1000" {
		t.Errorf("the accepted amount is %q, want \"1000\"", payload.Accepted.Amount)
	}
	if cosmosPayload.Authorization.To != testPayTo {
		t.Errorf("the recipient is %q, want %q", cosmosPayload.Authorization.To, testPayTo)
	}
	checkSignedTx(t, cosmosPayload.SignedTx, "mocha-5", testAddress, 42)
}

func TestClientReadsTheOptionFromTheBody(t *testing.T) {
	tests := []struct {
		name    string
		sidecar *fakeSidecar
	}{
		{"plain body", &fakeSidecar{price: "1000", inBodyOnly: true}},
		{"JSON-RPC body", &fakeSidecar{price: "1000", inBodyOnly: true, jsonRPCShape: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, sidecar, cfg := newTestClient(t, tt.sidecar, "")

			res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			res.Body.Close()
			if !receipt.Paid {
				t.Fatalf("the payer did not pay")
			}
			if _, payments := sidecar.counts(); payments != 1 {
				t.Errorf("payments = %d, want 1", payments)
			}
		})
	}
}

func TestClientStopsAfterOnePayment(t *testing.T) {
	client, sidecar, cfg := newTestClient(t, &fakeSidecar{price: "1000", reject: true}, "")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", res.StatusCode)
	}
	if receipt.Paid {
		t.Errorf("the receipt says paid, but the sidecar rejected the payment")
	}
	if requests, payments := sidecar.counts(); requests != 2 || payments != 1 {
		t.Errorf("requests = %d, payments = %d, want 2 and 1", requests, payments)
	}

	// The sidecar did not broadcast, so the money and the sequence go back.
	spent, count := client.Payer().Spent()
	if spent.Sign() != 0 || count != 0 {
		t.Errorf("spent = %s, payments = %d, want 0 and 0", spent, count)
	}
}

func TestClientRollsBackAFailedSettlement(t *testing.T) {
	client, _, cfg := newTestClient(t, &fakeSidecar{price: "1000", settleFails: true}, "")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()

	if receipt.Paid {
		t.Errorf("the receipt says paid, but the settlement failed")
	}
	spent, _ := client.Payer().Spent()
	if spent.Sign() != 0 {
		t.Errorf("spent = %s, want 0", spent)
	}
}

func TestClientDoesNotPayOnAnotherNetwork(t *testing.T) {
	client, sidecar, cfg := newTestClient(t, &fakeSidecar{price: "1000", network: "cosmos:celestia"}, "")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()

	if receipt.Paid {
		t.Errorf("the payer paid on another network")
	}
	if requests, payments := sidecar.counts(); requests != 1 || payments != 0 {
		t.Errorf("requests = %d, payments = %d, want 1 and 0", requests, payments)
	}
}

func TestClientDoesNotPayAboveTheLimit(t *testing.T) {
	client, sidecar, cfg := newTestClient(t, &fakeSidecar{price: "50000"}, "maxPerCall: \"1000\"\n")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/tx_search"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()

	if receipt.Paid {
		t.Errorf("the payer paid 50000, but the limit is 1000")
	}
	if _, payments := sidecar.counts(); payments != 0 {
		t.Errorf("payments = %d, want 0", payments)
	}
}

func TestClientPaysWithTheAccountOfTheSidecar(t *testing.T) {
	// This test uses no fake chain. The payer reads the account of the payer
	// from the sidecar over HTTP, so nothing in the path uses gRPC.
	sidecar := &fakeSidecar{price: "1000"}
	server := httptest.NewServer(sidecar)
	defer server.Close()

	body := "upstream: \"" + server.URL + "\"\n" + `
network: "cosmos:mocha-5"
mnemonic: "` + testMnemonic + `"
`
	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	wallet, err := WalletFromConfig(cfg)
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	accounts := NewAccounts(cfg.Upstream, cfg.Network)
	client := NewClient(cfg, NewPayer(cfg, wallet, accounts), log)

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()

	if !receipt.Paid {
		t.Fatalf("the payer did not pay")
	}
	if sidecar.accounts() != 1 {
		t.Errorf("the payer asked the account route %d times, want 1", sidecar.accounts())
	}

	// The sidecar gave the account number 42, so the signature must hold for
	// that number.
	_, cosmosPayload := sidecar.lastPayment(t)
	checkSignedTx(t, cosmosPayload.SignedTx, "mocha-5", testAddress, 42)
}

func TestClientWithoutAPayerGivesThe402Back(t *testing.T) {
	sidecar := &fakeSidecar{price: "1000"}
	server := httptest.NewServer(sidecar)
	defer server.Close()

	body := "upstream: \"" + server.URL + "\"\n" + `
network: "cosmos:mocha-5"
`
	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// A nil payer is what the -no-pay flag makes.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(cfg, nil, log)

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", res.StatusCode)
	}
	if receipt.Paid {
		t.Errorf("the payer paid, but it has no wallet")
	}

	// The caller must still get the payment option of the sidecar.
	if answer := readBody(t, res); answer["accepts"] == nil {
		t.Errorf("the 402 body has no payment option: %v", answer)
	}
	if requests, _ := sidecar.counts(); requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
}

func TestClientKeepsAPaymentWhoseAnswerDidNotArrive(t *testing.T) {
	// The sidecar takes the payment and closes the connection. It may have
	// broadcast the transaction, so the payer must keep the money against
	// the budget and keep the account sequence.
	client, sidecar, cfg := newTestClient(t, &fakeSidecar{price: "1000", dropPayment: true}, "")

	res, _, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err == nil {
		_ = res.Body.Close()
		t.Fatalf("the call ended with no error, but the sidecar gave no answer")
	}

	signer := client.Payer()
	spent, count := signer.Spent()
	if spent.String() != "3000" {
		t.Errorf("spent = %s, want 3000. The payer must count a payment that may be on the chain.", spent)
	}
	if count != 1 {
		t.Errorf("payments = %d, want 1", count)
	}
	open := signer.Open()
	if len(open) != 1 {
		t.Fatalf("Open() = %v, want 1 hash", open)
	}
	if len(open[0]) != 64 {
		t.Errorf("the hash is %q, want 64 characters", open[0])
	}

	// NOTE: The transport of Go sends an idempotent request again when the
	// connection closes before the answer starts, so the sidecar can see
	// the same payment more than 1 time. The replay guard of the sidecar
	// stops the second one. The payer signs 1 payment either way.
	if _, payments := sidecar.counts(); payments < 1 {
		t.Errorf("the sidecar got %d payments, want at least 1", payments)
	}
}

func TestClientGivesAPaymentBackWhenTheSidecarRefusesIt(t *testing.T) {
	// The sidecar answered, so it broadcast nothing. The money is still in
	// the wallet, and the payer must give the budget back.
	client, _, cfg := newTestClient(t, &fakeSidecar{price: "1000", reject: true}, "")

	res, receipt, err := client.Do(context.Background(), getFrom(cfg, "/block"))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if receipt.Paid {
		t.Errorf("the receipt says paid, but the sidecar refused the payment")
	}

	signer := client.Payer()
	spent, count := signer.Spent()
	if spent.Sign() != 0 {
		t.Errorf("spent = %s, want 0", spent)
	}
	if count != 0 {
		t.Errorf("payments = %d, want 0", count)
	}
	if open := signer.Open(); len(open) != 0 {
		t.Errorf("Open() = %v, want nothing. The sidecar answered.", open)
	}
}
