package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

// pricesServer answers /x402/prices with a fixed body.
func pricesServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x402/prices" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const testPrices = `{
  "network": "cosmos:mocha-5",
  "asset": "utia",
  "payTo": "celestia1kpr42yd4753cymc24ps78cm8dqf203qp0zwlgt",
  "scheme": "exact",
  "methods": {
    "status": "free",
    "block": "1000 utia",
    "tx_search": "10000 utia",
    "genesis": "not served"
  }
}`

func TestCostliest(t *testing.T) {
	tests := []struct {
		name    string
		methods map[string]string
		want    string
		amount  string
	}{
		{
			name:    "the costliest of 3",
			methods: map[string]string{"block": "1000 utia", "tx_search": "10000 utia", "commit": "2000 utia"},
			want:    "tx_search",
			amount:  "10000",
		},
		{
			name:    "it skips free and not served",
			methods: map[string]string{"status": "free", "genesis": "not served", "block": "1000 utia"},
			want:    "block",
			amount:  "1000",
		},
		{
			name:    "no method has a price",
			methods: map[string]string{"status": "free", "health": "free"},
			want:    "",
			amount:  "",
		},
		{
			name:    "a tie takes the first name",
			methods: map[string]string{"commit": "1000 utia", "block": "1000 utia"},
			want:    "block",
			amount:  "1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, amount := costliest(tt.methods)
			if name != tt.want {
				t.Errorf("method = %q, want %q", name, tt.want)
			}
			if tt.amount == "" {
				if amount != nil {
					t.Errorf("amount = %s, want nothing", amount)
				}
				return
			}
			if amount == nil || amount.String() != tt.amount {
				t.Errorf("amount = %v, want %s", amount, tt.amount)
			}
		})
	}
}

func TestInitWritesAFileThatLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	server := pricesServer(t, testPrices)

	if err := runInit(path, server.URL, false, io.Discard); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg, err := payer.LoadConfig(path)
	if err != nil {
		t.Fatalf("the file that init wrote does not load: %v", err)
	}
	if cfg.Upstream != server.URL {
		t.Errorf("upstream = %q, want %q", cfg.Upstream, server.URL)
	}
	if cfg.Network != "cosmos:mocha-5" {
		t.Errorf("network = %q, want \"cosmos:mocha-5\"", cfg.Network)
	}
	if cfg.MaxPerCall != "10000" {
		t.Errorf("maxPerCall = %q, want \"10000\"", cfg.MaxPerCall)
	}
	if cfg.Budget != "0" {
		t.Errorf("budget = %q, want \"0\"", cfg.Budget)
	}

	// The wallet must work.
	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		t.Fatalf("the wallet of init does not work: %v", err)
	}
	if !strings.HasPrefix(wallet.Address(), "celestia1") {
		t.Errorf("address = %q, want a celestia1… address", wallet.Address())
	}
}

func TestInitWritesBothFilesWithMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	server := pricesServer(t, testPrices)

	if err := runInit(path, server.URL, false, io.Discard); err != nil {
		t.Fatalf("init: %v", err)
	}

	for _, name := range []string{"config.yaml", ".mnemonic"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has the mode %o, want 600", name, perm)
		}
	}
}

func TestInitDoesNotWriteOverAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	server := pricesServer(t, testPrices)

	err := runInit(path, server.URL, false, io.Discard)
	if err == nil {
		t.Fatalf("init wrote over the file, but it must stop")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Errorf("error = %q, want it to hold \"exists\"", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(raw) != "keep me\n" {
		t.Errorf("the file changed: %q", raw)
	}

	// -force writes over it. The file keeps the mode of the write, not the
	// mode that it had.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := runInit(path, server.URL, true, io.Discard); err != nil {
		t.Fatalf("init -force: %v", err)
	}
	if _, err := payer.LoadConfig(path); err != nil {
		t.Fatalf("the file that -force wrote does not load: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.yaml has the mode %o after -force, want 600", perm)
	}
}

func TestInitRepairsTheModeOfAMnemonicThatExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	mnemonicPath := filepath.Join(dir, ".mnemonic")
	if err := os.WriteFile(mnemonicPath, []byte(testMnemonic+"\n"), 0o644); err != nil {
		t.Fatalf("write mnemonic: %v", err)
	}
	server := pricesServer(t, testPrices)

	out := &strings.Builder{}
	if err := runInit(path, server.URL, false, out); err != nil {
		t.Fatalf("init: %v", err)
	}

	info, err := os.Stat(mnemonicPath)
	if err != nil {
		t.Fatalf("stat mnemonic: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf(".mnemonic has the mode %o, want 600", perm)
	}
	if !strings.Contains(out.String(), "Changed the mode") {
		t.Errorf("init did not say that it changed the mode: %s", out)
	}

	raw, err := os.ReadFile(mnemonicPath)
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	if strings.TrimSpace(string(raw)) != testMnemonic {
		t.Errorf("init changed the mnemonic, but it must keep the wallet")
	}
}

func TestInitRejectsASidecarThatStartsWithADash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := runInit(path, "-force", false, io.Discard)
	if err == nil {
		t.Fatalf("init took \"-force\" as the address, but it must stop")
	}
	if !strings.Contains(err.Error(), "must not start with") {
		t.Errorf("error = %q, want it to hold \"must not start with\"", err)
	}
	if !strings.Contains(err.Error(), "Write the flags first") {
		t.Errorf("error = %q, want it to name the right order", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("init wrote %s, but it must write no file", path)
	}
}

func TestInitStopsWhenTheFileDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// An address without a scheme gives a file that no command can load.
	err := runInit(path, "localhost:26658", false, io.Discard)
	if err == nil {
		t.Fatalf("init ended with no error, but the file does not load")
	}
	if !strings.Contains(err.Error(), "must start with http") {
		t.Errorf("error = %q, want it to name the upstream", err)
	}
}

func TestInit4RunsAtTheSameTimeKeep1Wallet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	server := pricesServer(t, testPrices)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = runInit(path, server.URL, false, io.Discard)
		}()
	}
	wg.Wait()

	raw, err := os.ReadFile(filepath.Join(dir, ".mnemonic"))
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	if _, err := payer.WalletFromMnemonic(strings.TrimSpace(string(raw))); err != nil {
		t.Fatalf("4 init runs at the same time broke the wallet: %v", err)
	}
}

func TestInitKeepsAMnemonicThatExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	mnemonicPath := filepath.Join(dir, ".mnemonic")
	if err := os.WriteFile(mnemonicPath, []byte(testMnemonic+"\n"), 0o600); err != nil {
		t.Fatalf("write mnemonic: %v", err)
	}
	server := pricesServer(t, testPrices)

	if err := runInit(path, server.URL, false, io.Discard); err != nil {
		t.Fatalf("init: %v", err)
	}

	raw, err := os.ReadFile(mnemonicPath)
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	if strings.TrimSpace(string(raw)) != testMnemonic {
		t.Errorf("init made a second wallet. The mnemonic changed.")
	}

	cfg, err := payer.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	if wallet.Address() != testAddress {
		t.Errorf("address = %q, want %q", wallet.Address(), testAddress)
	}
}

func TestInitWritesTheFileWhenTheSidecarIsDown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	out := &strings.Builder{}
	if err := runInit(path, "http://127.0.0.1:1", false, out); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg, err := payer.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Network != "cosmos:mocha-5" {
		t.Errorf("network = %q, want the default \"cosmos:mocha-5\"", cfg.Network)
	}
	if cfg.MaxPerCall != "20000" {
		t.Errorf("maxPerCall = %q, want the default \"20000\"", cfg.MaxPerCall)
	}
	if !strings.Contains(out.String(), "did not answer") {
		t.Errorf("init did not say that the sidecar is down: %s", out)
	}
}

func TestInitWarnsAboutTheMainnet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := strings.Replace(testPrices, "cosmos:mocha-5", "cosmos:celestia", 1)
	server := pricesServer(t, body)

	out := &strings.Builder{}
	if err := runInit(path, server.URL, false, out); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out.String(), "CAUTION") {
		t.Errorf("init did not warn about the mainnet: %s", out)
	}
	if !strings.Contains(out.String(), "real TIA") {
		t.Errorf("the warning does not name real TIA: %s", out)
	}
}

func TestInitNamesTheConfigPathInTheNextStep(t *testing.T) {
	server := pricesServer(t, testPrices)

	// t.Chdir puts the directory back, and it stops a test that runs in
	// parallel with a loud error.
	//
	// The default path: the next command needs no -config flag.
	t.Chdir(t.TempDir())
	out1 := &strings.Builder{}
	if err := runInit("config.yaml", server.URL, false, out1); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out1.String(), "x402-celestia-rpc-payer fund") {
		t.Errorf("the output does not name fund: %s", out1)
	}
	if strings.Contains(out1.String(), "-config") {
		t.Errorf("the output names -config for the default path: %s", out1)
	}

	// A custom path: the next command needs the same -config flag, or it
	// reads the wrong file.
	t.Chdir(t.TempDir())
	out2 := &strings.Builder{}
	if err := runInit("custom.yaml", server.URL, false, out2); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out2.String(), "-config") {
		t.Errorf("the output does not name -config for a custom path: %s", out2)
	}
	if !strings.Contains(out2.String(), "custom.yaml") {
		t.Errorf("the output does not name the path: %s", out2)
	}
}

func TestInitStopsWhenTheSidecarAsksForAnotherToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := strings.Replace(testPrices, `"asset": "utia"`, `"asset": "ibc/ABCD"`, 1)
	server := pricesServer(t, body)

	err := runInit(path, server.URL, false, io.Discard)
	if err == nil {
		t.Fatalf("init wrote a file for another token, but the fee needs utia")
	}
	if !strings.Contains(err.Error(), "utia only") {
		t.Errorf("error = %q, want it to say that the payer pays utia only", err)
	}

	// init must write no file at all.
	for _, name := range []string{"config.yaml", ".mnemonic"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("init wrote %s, but it must write no file", name)
		}
	}
}
