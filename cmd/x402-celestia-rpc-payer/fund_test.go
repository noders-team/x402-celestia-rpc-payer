package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

func TestCallsFor(t *testing.T) {
	tests := []struct {
		balance    string
		maxPerCall string
		fee        string
		want       int64
	}{
		{"10000000", "10000", "2000", 833},
		{"12000", "10000", "2000", 1},
		{"11999", "10000", "2000", 0},
		{"0", "10000", "2000", 0},
		{"many", "10000", "2000", 0},
	}

	for _, tt := range tests {
		got := callsFor(tt.balance, tt.maxPerCall, tt.fee)
		if got != tt.want {
			t.Errorf("callsFor(%q, %q, %q) = %d, want %d",
				tt.balance, tt.maxPerCall, tt.fee, got, tt.want)
		}
	}
}

// testFunder builds a funder that reads from a list of balances.
func testFunder(balances []string, out *strings.Builder) *funder {
	cfg := &payer.Config{
		Upstream:   "http://localhost:26658",
		Network:    "cosmos:mocha-5",
		Asset:      "utia",
		MaxPerCall: "10000",
		Fee:        "2000",
	}
	step := 0
	return &funder{
		cfg:     cfg,
		address: testAddress,
		// The reader gives the balance at the index, then it counts up. The
		// last balance of the list comes back for every read after it.
		read: func(context.Context) (string, string, error) {
			balance := balances[step]
			if step < len(balances)-1 {
				step++
			}
			return balance, "utia", nil
		},
		out:      out,
		interval: time.Millisecond,
		limit:    2 * time.Second,
	}
}

func TestFundEndsAtOnceWhenTheWalletHasMoney(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"10000000"}, out)

	if err := f.run(context.Background()); err != nil {
		t.Fatalf("fund: %v", err)
	}

	if !strings.Contains(out.String(), testAddress) {
		t.Errorf("the output has no address: %s", out)
	}
	if !strings.Contains(out.String(), "833 calls") {
		t.Errorf("the output has no number of calls: %s", out)
	}
	if strings.Contains(out.String(), "Waiting") {
		t.Errorf("fund waited, but the wallet has money: %s", out)
	}
}

func TestFundWaitsForTheMoney(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"0", "0", "12000"}, out)

	if err := f.run(context.Background()); err != nil {
		t.Fatalf("fund: %v", err)
	}

	if !strings.Contains(out.String(), "waits for the money") {
		t.Errorf("fund did not wait: %s", out)
	}
	if !strings.Contains(out.String(), "1 calls") && !strings.Contains(out.String(), "1 call") {
		t.Errorf("the output has no number of calls: %s", out)
	}
}

func TestFundSaysWhatToDoWithNoFaucet(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"10000000"}, out)

	if err := f.run(context.Background()); err != nil {
		t.Fatalf("fund: %v", err)
	}

	if !strings.Contains(out.String(), "Send TIA to this address") {
		t.Errorf("the output does not say what to do: %s", out)
	}
	// The message must be correct on the mainnet, so it names no faucet.
	for _, word := range []string{"faucet", "Discord", "celestia-mocha.com"} {
		if strings.Contains(out.String(), word) {
			t.Errorf("the output names %q. It must work on the mainnet too: %s", word, out)
		}
	}
}

func TestFundStopsWhenTheTimeRunsOut(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"0"}, out)
	f.limit = 20 * time.Millisecond

	err := f.run(context.Background())
	if err == nil {
		t.Fatalf("fund ended with no error, but the money did not arrive")
	}
	if !strings.Contains(err.Error(), "did not arrive") {
		t.Errorf("error = %q, want it to hold \"did not arrive\"", err)
	}
}

func TestFundStopsWhenTheSidecarDoesNotAnswer(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"0"}, out)
	f.read = func(context.Context) (string, string, error) {
		return "", "", fmt.Errorf("the sidecar at http://localhost:26658 did not answer")
	}

	err := f.run(context.Background())
	if err == nil {
		t.Fatalf("fund ended with no error, but the sidecar is down")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("error = %q, want it to hold \"did not answer\"", err)
	}
}

func TestFundStopsWhenTheSidecarGivesNoBalance(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"0"}, out)
	f.limit = time.Hour
	// The reader of newFunder gives this error when the answer holds no
	// balance. The money may be there already, so fund must not wait.
	f.read = func(context.Context) (string, string, error) {
		return "", "", fmt.Errorf(
			"the sidecar at http://localhost:26658 did not give the balance of %s", testAddress)
	}

	done := make(chan error, 1)
	go func() { done <- f.run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("fund ended with no error, but the sidecar gave no balance")
		}
		if !strings.Contains(err.Error(), "did not give the balance") {
			t.Errorf("error = %q, want it to hold \"did not give the balance\"", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fund waits for the money, but the balance is not known")
	}

	if strings.Contains(out.String(), "waits for the money") {
		t.Errorf("fund waited: %s", out)
	}
}

func TestFundTakesTheDenomOfTheSidecar(t *testing.T) {
	out := &strings.Builder{}
	f := testFunder([]string{"10000000"}, out)
	f.cfg.Asset = "utia"
	f.read = func(context.Context) (string, string, error) {
		return "10000000", "urc20/tia", nil
	}

	if err := f.run(context.Background()); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if !strings.Contains(out.String(), "urc20/tia") {
		t.Errorf("the output does not name the denom of the sidecar: %s", out)
	}
}

// accountServer answers the account route of the sidecar with a fixed body.
func accountServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x402/account" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFundWaitsForAWalletThatTheChainDoesNotHold(t *testing.T) {
	// A new wallet has no account on the chain. fund exists for a new wallet,
	// so it must read 0 and wait, and it must not stop with an error.
	body := `{"error": "query account celestia1abc: rpc error: code = NotFound desc = account celestia1abc not found"}`
	server := accountServer(t, http.StatusNotFound, body)

	cfg := &payer.Config{
		Upstream:   server.URL,
		Network:    "cosmos:mocha-5",
		Asset:      "utia",
		MaxPerCall: "10000",
		Fee:        "2000",
	}
	out := &strings.Builder{}
	f := newFunder(cfg, testAddress, out)
	f.interval = time.Millisecond
	f.limit = 30 * time.Millisecond

	err := f.run(context.Background())
	if err == nil {
		t.Fatalf("fund ended with no error, but the money never arrived")
	}
	if !strings.Contains(err.Error(), "did not arrive") {
		t.Fatalf("error = %q, want the message of the time limit", err)
	}
	if !strings.Contains(out.String(), "Balance  0 utia") {
		t.Errorf("fund did not report a balance of 0: %s", out)
	}
	if !strings.Contains(out.String(), "waits for the money") {
		t.Errorf("fund did not wait: %s", out)
	}
}
