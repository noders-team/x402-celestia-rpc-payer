package payer

import (
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the complete configuration of the payer.
//
// The file is flat, and only 4 fields are decisions of a customer: Upstream,
// Network, MaxPerCall and Budget. Every other field has a good default, and a
// file that leaves it out gets that default. "init" writes the 4.
type Config struct {
	// Listen is the address of the local proxy, for example ":26659".
	Listen string `yaml:"listen"`
	// Upstream is the x402 sidecar that the payer pays.
	Upstream string `yaml:"upstream"`
	// Network is the CAIP-2 identifier, for example "cosmos:mocha-5".
	//
	// It must be the network that the sidecar asks for. The payer needs no
	// node of its own: it reads the account of the payer from the free
	// /x402/account route of the sidecar.
	Network string `yaml:"network"`

	// Mnemonic is the BIP-39 mnemonic of the payer wallet.
	Mnemonic string `yaml:"mnemonic"`
	// MnemonicFile is a file that holds the mnemonic. The payer reads it when
	// Mnemonic is empty. A relative path is read from the directory of the
	// configuration file.
	MnemonicFile string `yaml:"mnemonicFile"`

	// Asset is the only denom that the payer pays. The payer refuses a
	// payment option that asks for another token. The fee uses it too.
	Asset string `yaml:"asset"`
	// MaxPerCall is the highest price that the payer pays for 1 call, in the
	// atomic unit of Asset. It must be above 0.
	MaxPerCall string `yaml:"maxPerCall"`
	// Budget is the highest sum that the payer pays while it runs. It covers
	// the prices and the fees. A budget of "0" removes the limit.
	Budget string `yaml:"budget"`
	// Fee is the fee of the payment transaction, in the atomic unit of Asset.
	Fee string `yaml:"fee"`
	// GasLimit is the gas limit of the payment transaction.
	GasLimit uint64 `yaml:"gasLimit"`
	// TimeoutSeconds is how long a signed payment stays usable.
	TimeoutSeconds int `yaml:"timeoutSeconds"`
	// Memo goes into the payment transaction. Keep it empty.
	Memo string `yaml:"memo"`

	// dir is the directory of the configuration file. A relative
	// MnemonicFile is read from it.
	dir string
}

// oldBlocks are the 3 blocks of the configuration file of an earlier build.
var oldBlocks = []string{"celestia", "wallet", "payment"}

// DefaultConfigPath is the path that the -config flag has by default.
const DefaultConfigPath = "config.yaml"

// CommandName is the name of the binary. The messages of this package name it,
// so that a customer can copy the next command from the terminal.
const CommandName = "x402-celestia-rpc-payer"

// DefaultAsset is the only token that the payer pays.
//
// CAUTION: The fee of a Celestia transaction uses the denom of the payment.
// The chain takes a fee in utia only, so a payment in another token fails on
// the chain, with no local error.
const DefaultAsset = "utia"

// LoadConfig reads and checks a YAML configuration file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	if err := checkOldShape(path, raw); err != nil {
		return nil, err
	}

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.dir = filepath.Dir(path)

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &cfg, nil
}

// checkOldShape names the file of an earlier build.
//
// The strict decode stops at the first unknown field, and its message does not
// say what to do. This check runs before it.
func checkOldShape(path string, raw []byte) error {
	var loose map[string]any
	if err := yaml.Unmarshal(raw, &loose); err != nil {
		// The strict decode gives a better message for a broken file, so
		// this check gives the file to it.
		return nil //nolint:nilerr // the caller reports the real error
	}
	for _, block := range oldBlocks {
		if _, found := loose[block]; !found {
			continue
		}
		return fmt.Errorf(
			"%s has the old shape. The fields are flat now, and most of them are gone. "+
				"Write a new one with: %s", path, CommandFor(path, "init <sidecar>"))
	}
	return nil
}

// CommandFor names a command of the payer for a configuration file.
//
// A customer who uses another path must see the -config flag in the next
// command. Without the flag, that command reads config.yaml, not this file.
func CommandFor(path, command string) string {
	if path == DefaultConfigPath {
		return CommandName + " " + command
	}
	return CommandName + " -config " + path + " " + command
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":26659"
	}
	if c.Upstream == "" {
		c.Upstream = "http://localhost:26658"
	}
	if c.MnemonicFile == "" {
		c.MnemonicFile = ".mnemonic"
	}
	if c.Asset == "" {
		c.Asset = DefaultAsset
	}
	if c.MaxPerCall == "" {
		c.MaxPerCall = "20000"
	}
	if c.Budget == "" {
		c.Budget = "0"
	}
	if c.Fee == "" {
		c.Fee = "2000"
	}
	if c.GasLimit == 0 {
		c.GasLimit = 100000
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 120
	}
}

func (c *Config) validate() error {
	u, err := url.Parse(c.Upstream)
	if err != nil {
		return fmt.Errorf("upstream %q is not a URL: %w", c.Upstream, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("upstream %q must start with http:// or https://", c.Upstream)
	}
	if u.Host == "" {
		return fmt.Errorf("upstream %q has no host", c.Upstream)
	}

	if c.Network == "" {
		return fmt.Errorf("network is empty. Use a CAIP-2 id such as \"cosmos:mocha-5\"")
	}
	if !strings.HasPrefix(c.Network, "cosmos:") {
		return fmt.Errorf("network %q must start with \"cosmos:\"", c.Network)
	}

	maxPerCall, err := parseAmount("maxPerCall", c.MaxPerCall)
	if err != nil {
		return err
	}
	if maxPerCall.Sign() <= 0 {
		return fmt.Errorf("maxPerCall = %q must be above 0", c.MaxPerCall)
	}
	if _, err := parseAmount("budget", c.Budget); err != nil {
		return err
	}
	if _, err := parseAmount("fee", c.Fee); err != nil {
		return err
	}
	if c.GasLimit == 0 {
		return fmt.Errorf("gasLimit is 0")
	}

	// This function does not check the mnemonic. "-no-pay" runs with no
	// wallet, and this function cannot see a flag. newWallet reports the
	// error instead.
	return nil
}

// ReadMnemonic returns the mnemonic of the payer wallet.
func (c *Config) ReadMnemonic() (string, error) {
	if m := strings.TrimSpace(c.Mnemonic); m != "" {
		return m, nil
	}
	path := c.MnemonicPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read mnemonicFile %s: %w", path, err)
	}
	m := strings.TrimSpace(string(raw))
	if m == "" {
		return "", fmt.Errorf("mnemonicFile %s is empty", path)
	}
	return m, nil
}

// MnemonicPath returns the path of the mnemonic file.
func (c *Config) MnemonicPath() string {
	if filepath.IsAbs(c.MnemonicFile) {
		return c.MnemonicFile
	}
	return filepath.Join(c.dir, c.MnemonicFile)
}

// InsecureMnemonicFile reports if other users can read the mnemonic file.
func (c *Config) InsecureMnemonicFile() (string, bool) {
	path := c.MnemonicPath()
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	return path, info.Mode().Perm()&0o077 != 0
}

// MaxPerCallAmount returns the per-call limit as a number.
//
// It returns 0 when the value is not a number. validate stops such a file, so
// a 0 reaches a caller only when the caller built the Config itself. 0 refuses
// every price, which is the safe answer.
func (c *Config) MaxPerCallAmount() *big.Int {
	return amountOrZero(c.MaxPerCall)
}

// BudgetAmount returns the budget as a number. A budget of 0 means no limit.
// It returns 0 when the value is not a number.
func (c *Config) BudgetAmount() *big.Int {
	return amountOrZero(c.Budget)
}

// amountOrZero reads a number, and it gives 0 when the value is not a number.
// A nil *big.Int panics in Cmp, so these 3 methods never give one.
func amountOrZero(amount string) *big.Int {
	n, err := parseAmount("", amount)
	if err != nil {
		return big.NewInt(0)
	}
	return n
}

// ChainID returns the Cosmos chain-id of the configured network.
func (c *Config) ChainID() string {
	return strings.TrimPrefix(c.Network, "cosmos:")
}

// parseAmount reads a whole number that is 0 or above.
func parseAmount(field, amount string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(strings.TrimSpace(amount), 10)
	if !ok {
		return nil, fmt.Errorf("%s = %q is not a whole number", field, amount)
	}
	if n.Sign() < 0 {
		return nil, fmt.Errorf("%s = %q is below 0", field, amount)
	}
	return n, nil
}
