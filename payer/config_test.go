package payer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testMnemonic is a well-known BIP-39 test mnemonic. It holds no money.
const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// writeConfig writes a configuration file in a new directory.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const minimalConfig = `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
mnemonic: "` + testMnemonic + `"
`

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Listen != ":26659" {
		t.Errorf("listen = %q, want \":26659\"", cfg.Listen)
	}
	if cfg.MnemonicFile != ".mnemonic" {
		t.Errorf("mnemonicFile = %q, want \".mnemonic\"", cfg.MnemonicFile)
	}
	if cfg.Asset != "utia" {
		t.Errorf("asset = %q, want \"utia\"", cfg.Asset)
	}
	if cfg.Fee != "2000" {
		t.Errorf("fee = %q, want \"2000\"", cfg.Fee)
	}
	if cfg.GasLimit != 100000 {
		t.Errorf("gasLimit = %d, want 100000", cfg.GasLimit)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("timeoutSeconds = %d, want 120", cfg.TimeoutSeconds)
	}
	if cfg.ChainID() != "mocha-5" {
		t.Errorf("chain-id = %q, want \"mocha-5\"", cfg.ChainID())
	}
	if cfg.BudgetAmount().Sign() != 0 {
		t.Errorf("budget = %s, want 0", cfg.BudgetAmount())
	}
	if cfg.MaxPerCallAmount().String() != "20000" {
		t.Errorf("maxPerCall = %s, want 20000", cfg.MaxPerCallAmount())
	}
}

func TestLoadConfigNamesAnOldFile(t *testing.T) {
	old := `
upstream: "http://localhost:26658"
celestia:
  network: "cosmos:mocha-5"
payment:
  autoPay: true
`
	_, err := LoadConfig(writeConfig(t, old))
	if err == nil {
		t.Fatalf("the old file was accepted, but it must fail")
	}
	if !strings.Contains(err.Error(), "old shape") {
		t.Errorf("error = %q, want it to hold \"old shape\"", err)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("error = %q, want it to name the init command", err)
	}
}

func TestLoadConfigNamesAnOldFileWithTheWalletBlockAlone(t *testing.T) {
	old := `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
wallet:
  mnemonic: "` + testMnemonic + `"
`
	_, err := LoadConfig(writeConfig(t, old))
	if err == nil {
		t.Fatalf("the old file was accepted, but it must fail")
	}
	if !strings.Contains(err.Error(), "old shape") {
		t.Errorf("error = %q, want it to hold \"old shape\"", err)
	}
}

func TestLoadConfigNamesTheConfigFlagForAnotherPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	old := `
upstream: "http://localhost:26658"
celestia:
  network: "cosmos:mocha-5"
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatalf("the old file was accepted, but it must fail")
	}
	// init writes config.yaml. Without the flag, the customer writes the
	// wrong file.
	if !strings.Contains(err.Error(), "-config "+path+" init") {
		t.Errorf("error = %q, want it to name the -config flag", err)
	}
}

func TestAmountOrZero(t *testing.T) {
	// A nil *big.Int panics in Cmp, so a value that is not a number must
	// give 0.
	cfg := &Config{MaxPerCall: "many", Budget: "many"}
	if got := cfg.MaxPerCallAmount(); got == nil || got.Sign() != 0 {
		t.Errorf("maxPerCall = %v, want 0", got)
	}
	if got := cfg.BudgetAmount(); got == nil || got.Sign() != 0 {
		t.Errorf("budget = %v, want 0", got)
	}
}

func TestLoadConfigWithoutAMnemonic(t *testing.T) {
	// -no-pay runs with no wallet, so LoadConfig must not ask for one.
	body := `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
`
	if _, err := LoadConfig(writeConfig(t, body)); err != nil {
		t.Fatalf("load config: %v", err)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{
			// The reason names the field and the type, which only the
			// strict decode of the YAML package can say.
			name:   "a field that the Config does not have",
			body:   minimalConfig + "\nnotAField: 1\n",
			reason: "field notAField not found",
		},
		{
			name:   "network without prefix",
			body:   strings.Replace(minimalConfig, `"cosmos:mocha-5"`, `"mocha-5"`, 1),
			reason: "must start with",
		},
		{
			name:   "no network",
			body:   strings.Replace(minimalConfig, "network: \"cosmos:mocha-5\"\n", "", 1),
			reason: "network is empty",
		},
		{
			name:   "upstream without scheme",
			body:   strings.Replace(minimalConfig, `"http://localhost:26658"`, `"localhost:26658"`, 1),
			reason: "must start with http",
		},
		{
			name:   "maxPerCall of 0",
			body:   minimalConfig + "maxPerCall: \"0\"\n",
			reason: "must be above 0",
		},
		{
			name:   "negative budget",
			body:   minimalConfig + "budget: \"-1\"\n",
			reason: "below 0",
		},
		{
			name:   "budget that is not a number",
			body:   minimalConfig + "budget: \"many\"\n",
			reason: "not a whole number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tt.body))
			if err == nil {
				t.Fatalf("the config was accepted, but it must fail")
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error = %q, want it to hold %q", err, tt.reason)
			}
		})
	}
}

func TestMnemonicFileIsReadFromTheConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mnemonic"), []byte(testMnemonic+"\n"), 0o600); err != nil {
		t.Fatalf("write mnemonic: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	body := `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
mnemonicFile: ".mnemonic"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	got, err := cfg.ReadMnemonic()
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	if got != testMnemonic {
		t.Errorf("mnemonic = %q", got)
	}
}

func TestInsecureMnemonicFile(t *testing.T) {
	dir := t.TempDir()
	mnemonicPath := filepath.Join(dir, ".mnemonic")
	if err := os.WriteFile(mnemonicPath, []byte(testMnemonic), 0o644); err != nil {
		t.Fatalf("write mnemonic: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	body := `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
mnemonicFile: ".mnemonic"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	got, insecure := cfg.InsecureMnemonicFile()
	if !insecure {
		t.Fatalf("the file mode 0644 must be reported as readable by other users")
	}
	if got != mnemonicPath {
		t.Errorf("path = %q, want %q", got, mnemonicPath)
	}

	if err := os.Chmod(mnemonicPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, insecure := cfg.InsecureMnemonicFile(); insecure {
		t.Errorf("the file mode 0600 must be accepted")
	}
}
