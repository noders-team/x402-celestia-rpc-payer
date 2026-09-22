package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

// pricesRoute is the free route of the sidecar that gives the price list.
const pricesRoute = "/x402/prices"

// defaultNetwork is the network that init writes when it cannot read one.
const defaultNetwork = "cosmos:mocha-5"

// mainnetNetwork holds real money.
const mainnetNetwork = "cosmos:celestia"

// payerAsset is the only token that this payer pays.
//
// CAUTION: The fee of a Celestia transaction uses the denom of the payment.
// The chain takes a fee in utia only, so a payment in another token fails on
// the chain, with no local error. init stops before it writes such a file.
const payerAsset = payer.DefaultAsset

// sidecarPrices is the body of GET /x402/prices of the sidecar.
type sidecarPrices struct {
	Network string            `json:"network"`
	Asset   string            `json:"asset"`
	PayTo   string            `json:"payTo"`
	Methods map[string]string `json:"methods"`
}

// runInit writes a configuration file and a wallet.
//
// It reads the sidecar when it can, so that the file is correct on the first
// try. It never writes over a file, unless force is true, and it keeps a
// mnemonic file that exists.
func runInit(configPath, sidecar string, force bool, out io.Writer) error {
	// The flag package of Go stops at the first argument that is not a flag,
	// so a flag after the command reaches this function as the address.
	if strings.HasPrefix(sidecar, "-") {
		return errors.New("the sidecar address must not start with \"-\". " +
			"Write the flags first: x402-celestia-rpc-payer -force init <sidecar>")
	}
	if _, err := os.Stat(configPath); err == nil && !force {
		return fmt.Errorf("%s exists. Use -force to write over it", configPath)
	}

	network := defaultNetwork
	maxPerCall := "20000"

	if sidecar != "" {
		prices, err := readPrices(sidecar)
		if err != nil {
			fmt.Fprintf(out, "The sidecar at %s did not answer.\n", sidecar)
			fmt.Fprintf(out, "  %v\n\n", err)
		} else {
			if prices.Asset != "" && prices.Asset != payerAsset {
				return fmt.Errorf(
					"the sidecar asks for the token %q. This payer pays %s only",
					prices.Asset, payerAsset)
			}
			if prices.Network != "" {
				network = prices.Network
			}
			method, amount := costliest(prices.Methods)
			if amount != nil {
				maxPerCall = amount.String()
			}
			printPrices(out, prices, method, amount)
		}
	}

	upstream := sidecar
	if upstream == "" {
		upstream = "http://localhost:26658"
	}

	dir := filepath.Dir(configPath)
	mnemonicPath := filepath.Join(dir, ".mnemonic")
	file, err := makeMnemonic(mnemonicPath)
	if err != nil {
		return err
	}

	body := configFileBody(upstream, network, maxPerCall)
	if err := writeConfigFile(configPath, body, force); err != nil {
		return err
	}

	fmt.Fprintf(out, "Wrote %s\n", configPath)
	fmt.Fprintf(out, "  maxPerCall %s %s\n", maxPerCall, payerAsset)
	if file.made {
		fmt.Fprintf(out, "Wrote %s  mode 0600\n", mnemonicPath)
	} else {
		fmt.Fprintf(out, "Kept %s. The wallet does not change.\n", mnemonicPath)
	}
	if file.repaired {
		fmt.Fprintf(out, "Changed the mode of %s to 0600. Other users could read it.\n",
			mnemonicPath)
	}

	// Read the file back. A sidecar address that is not a URL gives a file
	// that no other command can load, and the customer must learn it here.
	if _, err := payer.LoadConfig(configPath); err != nil {
		fmt.Fprintf(out, "The file is not correct.\n")
		return err
	}

	wallet, err := payer.WalletFromMnemonic(file.mnemonic)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nYour address:\n  %s\n", wallet.Address())

	if network == mainnetNetwork {
		fmt.Fprintf(out, "\nCAUTION: %s is the Celestia mainnet. This wallet will hold\n", network)
		fmt.Fprint(out, "real TIA, and each call spends it.\n")
	}

	fmt.Fprintf(out, "\nNext: put TIA in it with\n  %s\n", payer.CommandFor(configPath, "fund"))
	return nil
}

// printPrices shows what init read from the sidecar.
func printPrices(out io.Writer, prices sidecarPrices, method string, amount *big.Int) {
	fmt.Fprint(out, "Read the sidecar.\n")
	fmt.Fprintf(out, "  network   %s\n", prices.Network)
	fmt.Fprintf(out, "  asset     %s\n", payerAsset)
	if prices.PayTo != "" {
		fmt.Fprintf(out, "  payTo     %s\n", prices.PayTo)
	}
	if amount != nil {
		fmt.Fprintf(out, "  costliest %s, %s %s\n", method, amount, payerAsset)
	} else {
		fmt.Fprint(out, "  costliest no method has a price\n")
	}
	fmt.Fprintln(out)
}

// readPrices asks the sidecar for its price list.
func readPrices(sidecar string) (sidecarPrices, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	target := strings.TrimRight(sidecar, "/") + pricesRoute
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return sidecarPrices{}, err
	}
	res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return sidecarPrices{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return sidecarPrices{}, fmt.Errorf("the sidecar answered %d", res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return sidecarPrices{}, err
	}
	var prices sidecarPrices
	if err := json.Unmarshal(body, &prices); err != nil {
		return sidecarPrices{}, fmt.Errorf("the answer is not readable: %w", err)
	}
	return prices, nil
}

// costliest returns the name and the price of the method that costs most.
//
// The price of a method is a string such as "1000 utia", "free", or "not
// served". costliest reads the first word, and it skips a value that is not a
// number. It returns an empty name and nil when no method has a price.
func costliest(methods map[string]string) (string, *big.Int) {
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	// Sort the names, so that a tie always gives the same answer.
	sort.Strings(names)

	var bestName string
	var best *big.Int
	for _, name := range names {
		fields := strings.Fields(methods[name])
		if len(fields) == 0 {
			continue
		}
		amount, ok := new(big.Int).SetString(fields[0], 10)
		if !ok || amount.Sign() <= 0 {
			continue
		}
		if best == nil || amount.Cmp(best) > 0 {
			best, bestName = amount, name
		}
	}
	return bestName, best
}

// mnemonicFile is what makeMnemonic did with the mnemonic file.
//
// CAUTION: mnemonic holds the key of the wallet. It must not reach the
// terminal, a log or an error message.
type mnemonicFile struct {
	// mnemonic is the BIP-39 mnemonic of the payer wallet.
	mnemonic string
	// made is true when makeMnemonic wrote a new file.
	made bool
	// repaired is true when makeMnemonic changed the mode of a file that
	// other users could read.
	repaired bool
}

// makeMnemonic writes a new mnemonic file, or it keeps a file that exists.
//
// NOTE: O_EXCL makes the file and the check of the file 1 step. 2 init runs at
// the same time then cannot cut the same wallet: 1 of the 2 gets an error of
// os.ErrExist, and it keeps the file of the other one.
func makeMnemonic(path string) (mnemonicFile, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return keepMnemonic(path)
		}
		return mnemonicFile{}, fmt.Errorf("write %s: %w", path, err)
	}

	mnemonic, err := payer.NewMnemonic()
	if err != nil {
		_ = file.Close()
		return mnemonicFile{}, err
	}
	if _, err := file.WriteString(mnemonic + "\n"); err != nil {
		_ = file.Close()
		return mnemonicFile{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return mnemonicFile{}, fmt.Errorf("write %s: %w", path, err)
	}
	return mnemonicFile{mnemonic: mnemonic, made: true}, nil
}

// keepMnemonic reads a mnemonic file that exists.
//
// It repairs the mode of the file when other users can read it. An earlier
// build made the file with the mode of the umask, and init is the 1 command
// that does not load the configuration file, so no other check sees it.
func keepMnemonic(path string) (mnemonicFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return mnemonicFile{}, fmt.Errorf("read %s: %w", path, err)
	}

	repaired := false
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return mnemonicFile{}, fmt.Errorf("change the mode of %s: %w", path, err)
		}
		repaired = true
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return mnemonicFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	mnemonic := strings.TrimSpace(string(raw))
	if mnemonic == "" {
		return mnemonicFile{}, fmt.Errorf("%s is empty. Remove it, and run init again", path)
	}
	return mnemonicFile{mnemonic: mnemonic, repaired: repaired}, nil
}

// writeConfigFile writes the configuration file with the mode 0600.
//
// Without force, O_EXCL stops the write when the file exists, so 2 init runs
// at the same time cannot cut the same file. With force, the write cuts the
// file, and the chmod gives the mode 0600 to a file that had another mode: the
// mode of OpenFile applies only when the call makes the file.
func writeConfigFile(path, body string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists. Use -force to write over it", path)
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("change the mode of %s: %w", path, err)
	}
	return nil
}

// configFileBody builds the configuration file that init writes.
//
// It holds the 4 fields that a customer decides. Every other field keeps its
// default, and config.example.yaml documents all of them.
func configFileBody(upstream, network, maxPerCall string) string {
	var b strings.Builder
	b.WriteString("# x402-celestia-rpc-payer. See config.example.yaml for every field.\n")
	b.WriteString("# The mnemonic of the wallet is in .mnemonic, next to this file.\n\n")
	fmt.Fprintf(&b, "upstream: %q\n", upstream)
	fmt.Fprintf(&b, "network: %q\n", network)
	fmt.Fprintf(&b, "\n# The highest price of 1 call, in %s. The payer refuses a call above it.\n", payerAsset)
	fmt.Fprintf(&b, "maxPerCall: %q\n", maxPerCall)
	fmt.Fprintf(&b, "\n# The highest sum while the payer runs, in %s. It covers the prices\n", payerAsset)
	b.WriteString("# and the fees. \"0\" removes the limit.\n")
	b.WriteString("budget: \"0\"\n")
	return b.String()
}
