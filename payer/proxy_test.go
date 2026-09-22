package payer

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noders-team/x402-celestia-rpc-payer/x402"
)

// newTestProxy builds the local proxy in front of a fake sidecar.
func newTestProxy(t *testing.T, sidecar *fakeSidecar, extra string) (*httptest.Server, *fakeSidecar, *Client) {
	t.Helper()

	client, sidecar, cfg := newTestClient(t, sidecar, extra)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewProxy(cfg, client, log))
	t.Cleanup(server.Close)
	return server, sidecar, client
}

func TestProxyPaysForACall(t *testing.T) {
	proxy, sidecar, _ := newTestProxy(t, &fakeSidecar{price: "1000"}, "")

	res, err := http.Get(proxy.URL + "/block?height=100")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if body := readBody(t, res); body["result"] != "paid" {
		t.Errorf("body = %v", body)
	}

	if requests, payments := sidecar.counts(); requests != 2 || payments != 1 {
		t.Errorf("requests = %d, payments = %d, want 2 and 1", requests, payments)
	}
	_, cosmosPayload := sidecar.lastPayment(t)
	checkSignedTx(t, cosmosPayload.SignedTx, "mocha-5", testAddress, 42)
}

func TestProxyGivesThePaymentResponseToTheCaller(t *testing.T) {
	proxy, _, _ := newTestProxy(t, &fakeSidecar{price: "1000"}, "")

	res, err := http.Get(proxy.URL + "/block")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	raw := res.Header.Get(x402.HeaderPaymentResponse)
	if raw == "" {
		t.Fatalf("the answer has no %s header", x402.HeaderPaymentResponse)
	}
	var pr x402.PaymentResponse
	if err := x402.DecodeHeader(raw, &pr); err != nil {
		t.Fatalf("decode the header: %v", err)
	}
	if pr.TransactionHash != "ABCD1234" {
		t.Errorf("transaction hash = %q, want \"ABCD1234\"", pr.TransactionHash)
	}
}

func TestProxySendsTheBodyAgainWithThePayment(t *testing.T) {
	proxy, sidecar, _ := newTestProxy(t, &fakeSidecar{price: "1000", jsonRPCShape: true}, "")

	body := `{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":"100"}}`
	res, err := http.Post(proxy.URL+"/", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}

	bodies := sidecar.sentBodies()
	if len(bodies) != 2 {
		t.Fatalf("the sidecar got %d requests, want 2", len(bodies))
	}
	for i, got := range bodies {
		if got != body {
			t.Errorf("request %d carried %q, want %q", i+1, got, body)
		}
	}
}

func TestProxyKeepsThePathAndTheQuery(t *testing.T) {
	var got string
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RequestURI()
		writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
	}))
	defer sidecar.Close()

	body := "upstream: \"" + sidecar.URL + "\"\n" + `
network: "cosmos:mocha-5"
`
	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy := httptest.NewServer(NewProxy(cfg, NewClient(cfg, nil, log), log))
	defer proxy.Close()

	res, err := http.Get(proxy.URL + `/tx_search?query=%22tx.height%3D5%22&page=1`)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	res.Body.Close()

	want := `/tx_search?query=%22tx.height%3D5%22&page=1`
	if got != want {
		t.Errorf("the sidecar got %q, want %q", got, want)
	}
}

func TestProxyGivesA402BackWithoutAPayer(t *testing.T) {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy := httptest.NewServer(NewProxy(cfg, NewClient(cfg, nil, log), log))
	defer proxy.Close()

	res, err := http.Get(proxy.URL + "/block")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if res.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", res.StatusCode)
	}
	if answer := readBody(t, res); answer["accepts"] == nil {
		t.Errorf("the 402 body has no payment option: %v", answer)
	}
	if res.Header.Get(x402.HeaderPaymentRequired) == "" {
		t.Errorf("the answer has no %s header", x402.HeaderPaymentRequired)
	}

	// The proxy has no payer, so it must not try to pay.
	if requests, _ := sidecar.counts(); requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
}

func TestProxyStatus(t *testing.T) {
	proxy, sidecar, _ := newTestProxy(t, &fakeSidecar{price: "1000"}, "budget: \"5000\"\n")

	first, err := http.Get(proxy.URL + "/block")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = first.Body.Close()

	res, err := http.Get(proxy.URL + "/x402/payer/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	status := readBody(t, res)

	if status["payer"] != testAddress {
		t.Errorf("payer = %v, want %q", status["payer"], testAddress)
	}
	// The price of 1000 and the fee of 2000 left the wallet.
	if status["spent"] != "3000" {
		t.Errorf("spent = %v, want \"3000\"", status["spent"])
	}
	if status["budget"] != "5000" {
		t.Errorf("budget = %v, want \"5000\"", status["budget"])
	}

	// The status route belongs to the proxy. It does not reach the sidecar.
	if requests, _ := sidecar.counts(); requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestProxyHealth(t *testing.T) {
	proxy, _, _ := newTestProxy(t, &fakeSidecar{}, "")

	res, err := http.Get(proxy.URL + "/x402/payer/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if body := readBody(t, res); body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestProxyRejectsAnUnknownOwnRoute(t *testing.T) {
	proxy, sidecar, _ := newTestProxy(t, &fakeSidecar{}, "")

	res, err := http.Get(proxy.URL + "/x402/payer/unknown")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	res.Body.Close()
	if requests, _ := sidecar.counts(); requests != 0 {
		t.Errorf("requests = %d, want 0", requests)
	}
}

func TestProxyDoesNotCarryWebsocket(t *testing.T) {
	proxy, sidecar, _ := newTestProxy(t, &fakeSidecar{}, "")

	res, err := http.Get(proxy.URL + "/websocket")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if res.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", res.StatusCode)
	}
	res.Body.Close()
	if requests, _ := sidecar.counts(); requests != 0 {
		t.Errorf("requests = %d, want 0", requests)
	}
}

func TestProxyReportsASidecarThatIsDown(t *testing.T) {
	body := `
upstream: "http://127.0.0.1:1"
network: "cosmos:mocha-5"
`
	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy := httptest.NewServer(NewProxy(cfg, NewClient(cfg, nil, log), log))
	defer proxy.Close()

	res, err := http.Get(proxy.URL + "/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse the body: %v", err)
	}
	if !strings.Contains(out["error"], "did not answer") {
		t.Errorf("error = %q, want it to hold \"did not answer\"", out["error"])
	}
}
