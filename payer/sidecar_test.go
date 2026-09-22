package payer

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/noders-team/x402-celestia-rpc-payer/x402"
)

// fakeSidecar is an x402 sidecar that answers from memory.
type fakeSidecar struct {
	// price is the price of a call. A price of "" makes the call free.
	price string
	// reject answers 402 again when the payer sends a payment.
	reject bool
	// settleFails answers 402 with a failed Payment-Response.
	settleFails bool
	// inBodyOnly leaves out the Payment-Required header.
	inBodyOnly bool
	// jsonRPCShape puts the payment option in a JSON-RPC error.
	jsonRPCShape bool
	// network is the network of the payment option.
	network string

	mu           sync.Mutex
	requests     int
	accountCalls int
	payments     []string
	bodies       []string
}

func (s *fakeSidecar) option() x402.PaymentOption {
	network := s.network
	if network == "" {
		network = "cosmos:mocha-5"
	}
	return x402.PaymentOption{
		Scheme:            x402.SchemeExact,
		Network:           network,
		Amount:            s.price,
		Asset:             "utia",
		PayTo:             testPayTo,
		MaxTimeoutSeconds: 120,
		Description:       "One call of the Celestia RPC method",
	}
}

func (s *fakeSidecar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The account route is free, and it needs no payment.
	if r.URL.Path == accountRoute {
		s.mu.Lock()
		s.accountCalls++
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, AccountAnswer{
			Address:       r.URL.Query().Get("address"),
			Network:       s.option().Network,
			ChainID:       "mocha-5",
			AccountNumber: "42",
			Sequence:      "7",
			Denom:         "utia",
			Balance:       "499985000",
		})
		return
	}

	body, _ := io.ReadAll(r.Body)
	header := r.Header.Get(x402.HeaderPaymentSignature)

	s.mu.Lock()
	s.requests++
	s.bodies = append(s.bodies, string(body))
	if header != "" {
		s.payments = append(s.payments, header)
	}
	s.mu.Unlock()

	if s.price == "" {
		writeJSON(w, http.StatusOK, map[string]any{"result": "free"})
		return
	}

	if header == "" {
		s.sendRequired(w, "payment_required")
		return
	}

	if s.reject {
		s.sendResponse(w, x402.PaymentResponse{Status: "failed", Error: "invalid signature"})
		s.sendRequired(w, "invalid signature")
		return
	}
	if s.settleFails {
		s.sendResponse(w, x402.PaymentResponse{Status: "failed", Error: "out of gas"})
		writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "out of gas"})
		return
	}

	s.sendResponse(w, x402.PaymentResponse{
		Status:          "success",
		TransactionHash: "ABCD1234",
		Network:         s.option().Network,
		Payer:           testAddress,
	})
	writeJSON(w, http.StatusOK, map[string]any{"result": "paid"})
}

// sendRequired answers 402 with the payment option.
func (s *fakeSidecar) sendRequired(w http.ResponseWriter, reason string) {
	required := x402.PaymentRequiredV2{
		X402Version: x402.VersionV2,
		Error:       reason,
		Accepts:     []x402.PaymentOption{s.option()},
	}
	if !s.inBodyOnly {
		if encoded, err := x402.EncodeHeader(required); err == nil {
			w.Header().Set(x402.HeaderPaymentRequired, encoded)
		}
	}
	if s.jsonRPCShape {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]any{"code": -32000, "message": reason, "data": required},
		})
		return
	}
	writeJSON(w, http.StatusPaymentRequired, required)
}

func (s *fakeSidecar) sendResponse(w http.ResponseWriter, pr x402.PaymentResponse) {
	if encoded, err := x402.EncodeHeader(pr); err == nil {
		w.Header().Set(x402.HeaderPaymentResponse, encoded)
	}
}

func (s *fakeSidecar) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, len(s.payments)
}

func (s *fakeSidecar) lastPayment(t *testing.T) (x402.PaymentPayloadV2, x402.CosmosPayload) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.payments) == 0 {
		t.Fatalf("the payer sent no payment")
	}
	return decodePayment(t, s.payments[len(s.payments)-1])
}

func (s *fakeSidecar) accounts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accountCalls
}

func (s *fakeSidecar) sentBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// newTestClient builds a client that talks to a fake sidecar.
func newTestClient(t *testing.T, sidecar *fakeSidecar, extra string) (*Client, *fakeSidecar, *Config) {
	t.Helper()

	server := httptest.NewServer(sidecar)
	t.Cleanup(server.Close)

	body := "upstream: \"" + server.URL + "\"\n" + `
network: "cosmos:mocha-5"
mnemonic: "` + testMnemonic + `"
` + extra

	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wallet, err := WalletFromConfig(cfg)
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	chain := &fakeChain{number: 42, sequence: 7}
	return NewClient(cfg, NewPayer(cfg, wallet, chain.Account), log), sidecar, cfg
}

// readBody reads and closes the body of an answer.
func readBody(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse the body %q: %v", raw, err)
	}
	return out
}
