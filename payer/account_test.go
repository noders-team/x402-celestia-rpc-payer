package payer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// accountHandler answers the account route with a fixed body.
func accountHandler(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != accountRoute {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("address"); got != testAddress {
			t.Errorf("the route got the address %q, want %q", got, testAddress)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const goodAccount = `{
  "address": "celestia19rl4cm2hmr8afy4kldpxz3fka4jguq0ad2ud9c",
  "network": "cosmos:mocha-5",
  "chainId": "mocha-5",
  "accountNumber": "42",
  "sequence": "7",
  "denom": "utia",
  "balance": "499985000"
}`

func TestAccountsReadTheNumberAndTheSequence(t *testing.T) {
	server := accountHandler(t, http.StatusOK, goodAccount)
	accounts := NewAccounts(server.URL, "cosmos:mocha-5")

	info, err := accounts.Account(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if info.Number != 42 {
		t.Errorf("number = %d, want 42", info.Number)
	}
	if info.Sequence != 7 {
		t.Errorf("sequence = %d, want 7", info.Sequence)
	}

	answer, err := accounts.Read(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if answer.Balance != "499985000" {
		t.Errorf("balance = %q, want \"499985000\"", answer.Balance)
	}
	if answer.ChainID != "mocha-5" {
		t.Errorf("chainId = %q, want \"mocha-5\"", answer.ChainID)
	}
}

func TestAccountsRejects(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{
			name:   "another network",
			status: http.StatusOK,
			body:   strings.Replace(goodAccount, "cosmos:mocha-5", "cosmos:celestia", 1),
			reason: "but the payer pays on",
		},
		{
			name:   "an account number that is not a number",
			status: http.StatusOK,
			body:   strings.Replace(goodAccount, `"accountNumber": "42"`, `"accountNumber": "many"`, 1),
			reason: "which is not a number",
		},
		{
			name:   "a sequence that is not a number",
			status: http.StatusOK,
			body:   strings.Replace(goodAccount, `"sequence": "7"`, `"sequence": "soon"`, 1),
			reason: "which is not a number",
		},
		{
			name:   "no account number",
			status: http.StatusOK,
			body:   `{"network": "cosmos:mocha-5"}`,
			reason: "gave no account number",
		},
		{
			name:   "a body that is not JSON",
			status: http.StatusOK,
			body:   "not json",
			reason: "is not readable",
		},
		{
			name:   "an address that the chain does not hold",
			status: http.StatusNotFound,
			body:   `{"error": "account celestia1… not found"}`,
			reason: "not found",
		},
		{
			name:   "a sidecar with no route",
			status: http.StatusNotFound,
			body:   "",
			reason: "has no /x402/account route",
		},
		{
			name:   "a sidecar with no endpoint",
			status: http.StatusServiceUnavailable,
			body:   `{"error": "this sidecar has no celestia.grpc endpoint"}`,
			reason: "cannot read an account",
		},
		{
			name:   "an error of the sidecar",
			status: http.StatusInternalServerError,
			body:   `{"error": "broken"}`,
			reason: "answered 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := accountHandler(t, tt.status, tt.body)
			accounts := NewAccounts(server.URL, "cosmos:mocha-5")

			_, err := accounts.Account(context.Background(), testAddress)
			if err == nil {
				t.Fatalf("the answer was accepted, but it must fail")
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error = %q, want it to hold %q", err, tt.reason)
			}
		})
	}
}

func TestAccountsReportASidecarThatIsDown(t *testing.T) {
	accounts := NewAccounts("http://127.0.0.1:1", "cosmos:mocha-5")

	_, err := accounts.Account(context.Background(), testAddress)
	if err == nil {
		t.Fatalf("the answer was accepted, but the sidecar is down")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("error = %q, want it to hold \"did not answer\"", err)
	}
}

func TestAccountsNameAWalletThatTheChainDoesNotHold(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		noAccount bool
	}{
		{
			name:      "the node says not found",
			body:      `{"error": "query account celestia1abc: rpc error: code = NotFound desc = account celestia1abc not found"}`,
			noAccount: true,
		},
		{
			name:      "the sidecar says not on the chain",
			body:      `{"error": "the account celestia1abc is not on the chain. Send it TIA first"}`,
			noAccount: true,
		},
		{
			name:      "the node did not answer",
			body:      `{"error": "query account celestia1abc: rpc error: code = Unavailable desc = connection error: network is unreachable"}`,
			noAccount: false,
		},
		{
			name:      "the sidecar has no route",
			body:      "",
			noAccount: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := accountHandler(t, http.StatusNotFound, tt.body)
			accounts := NewAccounts(server.URL, "cosmos:mocha-5")

			_, err := accounts.Read(context.Background(), testAddress)
			if err == nil {
				t.Fatalf("the answer was accepted, but the sidecar answered 404")
			}
			if got := errors.Is(err, ErrNoAccount); got != tt.noAccount {
				t.Errorf("errors.Is(err, ErrNoAccount) = %v, want %v. The error is %q",
					got, tt.noAccount, err)
			}
		})
	}
}
