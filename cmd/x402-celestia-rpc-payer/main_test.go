package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

func TestQuoteParam(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"100", "100"},
		{"-1", "-1"},
		{"true", "true"},
		{"false", "false"},
		{"0xABCD", "0xABCD"},
		{`"tx.height=5"`, `"tx.height=5"`},
		{"tx.height=5", `"tx.height=5"`},
		{"", `""`},
	}

	for _, tt := range tests {
		if got := quoteParam(tt.in); got != tt.want {
			t.Errorf("quoteParam(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseParams(t *testing.T) {
	params, err := parseParams([]string{"height=100", "prove=true", "query=tx.height=5"})
	if err != nil {
		t.Fatalf("parse params: %v", err)
	}
	want := map[string]string{"height": "100", "prove": "true", "query": `"tx.height=5"`}
	for key, value := range want {
		if params[key] != value {
			t.Errorf("params[%q] = %q, want %q", key, params[key], value)
		}
	}
}

func TestParseParamsRejectsAnArgumentWithoutAnEqualSign(t *testing.T) {
	_, err := parseParams([]string{"height"})
	if err == nil {
		t.Fatalf("the argument was accepted, but it must fail")
	}
	if !strings.Contains(err.Error(), "key=value") {
		t.Errorf("error = %q, want it to hold \"key=value\"", err)
	}
}

func TestBuildCallURIStyle(t *testing.T) {
	cfg := &payer.Config{Upstream: "http://localhost:26658"}
	build := buildCall(cfg, "block", map[string]string{"height": "100"}, false)

	req, err := build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if req.URL.Path != "/block" {
		t.Errorf("path = %q, want \"/block\"", req.URL.Path)
	}
	if req.URL.Query().Get("height") != "100" {
		t.Errorf("query = %q", req.URL.RawQuery)
	}
}

func TestBuildCallJSONRPC(t *testing.T) {
	cfg := &payer.Config{Upstream: "http://localhost:26658"}
	build := buildCall(cfg, "block", map[string]string{"height": "100"}, true)

	// The builder must give a new body each time, because the payer sends
	// the request 1 more time with the payment.
	for i := 0; i < 2; i++ {
		req, err := build()
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if req.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", req.Method)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read the body: %v", err)
		}
		var out struct {
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("parse the body %q: %v", body, err)
		}
		if out.Method != "block" || out.Params["height"] != "100" {
			t.Errorf("body = %s", body)
		}
	}
}

// noPayConfig names a mnemonic file that is not there. -no-pay must not read
// a wallet, so it must load all the same.
const noPayConfig = `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
mnemonicFile: "no-such-file"
`

func TestNewRuntimeWithNoPayReadsNoWallet(t *testing.T) {
	cfg, err := payer.LoadConfig(writeConfig(t, noPayConfig))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	rt, err := newRuntime(cfg, testLogger(), true)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if rt.client.Payer() != nil {
		t.Errorf("the runtime has a payer, but -no-pay is set")
	}
}

func TestNewRuntimeBuildsThePayer(t *testing.T) {
	cfg, err := payer.LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	rt, err := newRuntime(cfg, testLogger(), false)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	signer := rt.client.Payer()
	if signer == nil {
		t.Fatalf("the runtime has no payer")
	}
	if signer.Address() != testAddress {
		t.Errorf("payer = %q, want %q", signer.Address(), testAddress)
	}
}

func TestNewRuntimeNeedsAWalletWhenItPays(t *testing.T) {
	cfg, err := payer.LoadConfig(writeConfig(t, noPayConfig))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if _, err := newRuntime(cfg, testLogger(), false); err == nil {
		t.Fatalf("the runtime was built, but the mnemonic file is not there")
	}
}

func TestRunInitRejectsAFlagAfterTheCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := run("init", []string{"-force"}, path, "info", false, false, false)
	if err == nil {
		t.Fatalf("run took \"-force\" as the address, but it must stop")
	}
	if !strings.Contains(err.Error(), "must not start with") {
		t.Errorf("error = %q, want it to hold \"must not start with\"", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("run wrote %s, but it must write no file", path)
	}
}

func TestRunReportsACommandThatIsNotKnown(t *testing.T) {
	err := run("fly", nil, writeConfig(t, minimalConfig), "error", false, false, false)
	if err == nil {
		t.Fatalf("the command was accepted, but it is not known")
	}
	if !strings.Contains(err.Error(), "not known") {
		t.Errorf("error = %q, want it to hold \"not known\"", err)
	}
}

// testLogger writes nothing.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
