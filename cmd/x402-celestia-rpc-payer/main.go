// Command x402-celestia-rpc-payer is the payer of an x402 Celestia
// RPC sidecar.
//
// The payer has 2 shapes:
//
//   - A local proxy. A tool connects to it in the same way that it connects to
//     a CometBFT RPC. The proxy sends each call to the sidecar. When the
//     sidecar answers 402, the proxy signs a payment in TIA and sends the call
//     again.
//   - A command line. It does 1 call, or it reads the wallet and the sidecar.
//
// The mnemonic of the payer wallet is in .mnemonic, next to the configuration
// file. "init" writes it there with the mode 0600. The field "mnemonic:" of
// the configuration file is the other way.
//
// Usage:
//
//	x402-celestia-rpc-payer -config config.yaml serve
//	x402-celestia-rpc-payer -config config.yaml call block height=100
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/noders-team/x402-celestia-rpc-payer/payer"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	fs := flag.NewFlagSet("x402-celestia-rpc-payer", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", payer.DefaultConfigPath, "path of the YAML configuration file")
	logLevel := fs.String("log-level", "info", "debug, info, warn, or error")
	jsonRPC := fs.Bool("json", false, "call: use a JSON-RPC body instead of the path")
	noPay := fs.Bool("no-pay", false, "serve, call and check: do not pay. Give the 402 answer to the caller.")
	force := fs.Bool("force", false, "init: write over a config.yaml that exists")
	showVersion := fs.Bool("version", false, "print the version, the same as the version command")
	fs.Usage = usage

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := fs.Args()
	// Many users type "--version" before they read the usage. The flag does
	// what the command does, and it ignores the rest of the arguments.
	if *showVersion {
		args = []string{"version"}
	}
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	if err := run(args[0], args[1:], *configPath, *logLevel, *jsonRPC, *noPay, *force); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(command string, args []string, configPath, logLevel string, jsonRPC, noPay, force bool) error {
	// These 2 commands need no configuration file.
	switch command {
	case "version":
		fmt.Println("x402-celestia-rpc-payer", version)
		return nil
	case "init":
		sidecar := ""
		if len(args) > 0 {
			sidecar = args[0]
		}
		return runInit(configPath, sidecar, force, os.Stdout)
	}

	log := newLogger(logLevel)

	cfg, err := payer.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if path, insecure := cfg.InsecureMnemonicFile(); insecure {
		log.Warn("other users can read the mnemonic file. Run: chmod 600 "+path, "file", path)
	}

	switch command {
	case "check":
		return runCheck(cfg, noPay)
	case "address":
		return runAddress(cfg)
	case "serve":
		return runServe(cfg, log, noPay)
	case "balance":
		return runBalance(cfg)
	case "fund":
		return runFund(cfg)
	case "prices":
		return runGet(cfg, "/x402/prices")
	case "health":
		return runGet(cfg, "/x402/health")
	case "call":
		return runCall(cfg, log, args, jsonRPC, noPay)
	default:
		usage()
		return fmt.Errorf("the command %q is not known", command)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `x402-celestia-rpc-payer — the payer of an x402 Celestia RPC sidecar.

Usage:
  x402-celestia-rpc-payer [flags] <command> [arguments]

Commands:
  serve                     start the local proxy that pays for each call
  call <method> [k=v ...]   do 1 call and print the answer
  address                   print the address of the payer wallet
  balance                   print the balance of the payer wallet
  fund                      show the address and wait for the money
  prices                    print the price of each method of the sidecar
  health                    check the sidecar
  check                     read the configuration and print it
  init [sidecar]            write config.yaml and a wallet
  version                   print the version

Flags:
  -config string      path of the YAML configuration file (default "config.yaml")
  -log-level string   debug, info, warn, or error (default "info")
  -json               call: use a JSON-RPC body instead of the path
  -no-pay             serve, call and check: do not pay, and give the 402 to the caller
  -force              init: write over a config.yaml that exists
  -version            print the version, the same as the version command

Examples:
  x402-celestia-rpc-payer init http://localhost:26658
  x402-celestia-rpc-payer -config config.yaml check
  x402-celestia-rpc-payer -config config.yaml call status
  x402-celestia-rpc-payer -config config.yaml call block height=100
  x402-celestia-rpc-payer -config config.yaml serve
`)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

// runtime holds the parts that a paid call needs.
type runtime struct {
	client *payer.Client
}

// newRuntime builds the wallet and the paying Client.
//
// With noPay, the runtime reads no wallet. The Client then gives every 402
// answer of the sidecar to the caller.
func newRuntime(cfg *payer.Config, log *slog.Logger, noPay bool) (*runtime, error) {
	if noPay {
		log.Warn("-no-pay is set. The payer does not pay.")
		return &runtime{client: payer.NewClient(cfg, nil, log)}, nil
	}

	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	accounts := payer.NewAccounts(cfg.Upstream, cfg.Network)
	signer := payer.NewPayer(cfg, wallet, accounts)
	return &runtime{client: payer.NewClient(cfg, signer, log)}, nil
}

// --- commands --------------------------------------------------------------

// runCheck prints the configuration.
//
// With noPay, it reads no wallet. A customer who runs the payer with -no-pay
// has no mnemonic, and check must work for that customer too.
func runCheck(cfg *payer.Config, noPay bool) error {
	fmt.Println("listen     ", cfg.Listen)
	fmt.Println("upstream   ", cfg.Upstream)
	fmt.Println("network    ", cfg.Network)
	fmt.Println("chain-id   ", cfg.ChainID())
	fmt.Println("asset      ", cfg.Asset)
	fmt.Println("maxPerCall ", cfg.MaxPerCall, cfg.Asset)
	if cfg.BudgetAmount().Sign() == 0 {
		fmt.Println("budget      no limit")
	} else {
		fmt.Println("budget     ", cfg.Budget, cfg.Asset)
	}
	fmt.Println("fee        ", cfg.Fee, cfg.Asset)
	fmt.Println("gasLimit   ", cfg.GasLimit)

	if noPay {
		fmt.Println("payer       none. -no-pay is set, so the payer does not pay.")
		fmt.Println("\nThe configuration is correct.")
		return nil
	}

	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		return err
	}
	fmt.Println("payer      ", wallet.Address())

	fmt.Println("\nThe configuration is correct.")
	return nil
}

// runAddress prints the address of the payer wallet.
func runAddress(cfg *payer.Config) error {
	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		return err
	}
	fmt.Println(wallet.Address())
	return nil
}

// runBalance prints the balance of the payer wallet.
//
// The sidecar reads it from the chain, because the payer has no node.
func runBalance(cfg *payer.Config) error {
	wallet, err := payer.WalletFromConfig(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	answer, err := payer.NewAccounts(cfg.Upstream, cfg.Network).Read(ctx, wallet.Address())
	if errors.Is(err, payer.ErrNoAccount) {
		// A balance of 0 is a fact, not a fault. The chain makes the
		// account when the address gets money for the first time.
		fmt.Printf("%s 0 %s\n", wallet.Address(), cfg.Asset)
		fmt.Fprintln(os.Stderr, "The chain does not hold this account yet. Send it TIA.")
		return nil
	}
	if err != nil {
		return err
	}
	if answer.Balance == "" {
		return fmt.Errorf("the sidecar at %s did not give the balance of %s",
			cfg.Upstream, wallet.Address())
	}

	denom := answer.Denom
	if denom == "" {
		denom = cfg.Asset
	}
	fmt.Printf("%s %s %s\n", wallet.Address(), answer.Balance, denom)
	return nil
}

// runGet reads a free route of the sidecar and prints the answer.
func runGet(cfg *payer.Config, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	target := strings.TrimRight(cfg.Upstream, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("the sidecar at %s did not answer: %w", cfg.Upstream, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	printJSON(body)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the sidecar answered %d", res.StatusCode)
	}
	return nil
}

// runCall does 1 RPC call and prints the answer.
func runCall(cfg *payer.Config, log *slog.Logger, args []string, jsonRPC, noPay bool) error {
	if len(args) == 0 {
		return errors.New("call needs a method, for example: call block height=100")
	}
	method := args[0]
	params, err := parseParams(args[1:])
	if err != nil {
		return err
	}

	rt, err := newRuntime(cfg, log, noPay)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	build := buildCall(cfg, method, params, jsonRPC)
	res, receipt, err := rt.client.Do(ctx, build)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	if receipt.Paid {
		fmt.Fprintf(os.Stderr, "paid %s %s", receipt.Amount, receipt.Asset)
		if receipt.Response != nil && receipt.Response.TransactionHash != "" {
			fmt.Fprintf(os.Stderr, ", tx %s", receipt.Response.TransactionHash)
		}
		fmt.Fprintln(os.Stderr)
	}

	printJSON(body)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the sidecar answered %d", res.StatusCode)
	}
	return nil
}

// runServe starts the local proxy.
func runServe(cfg *payer.Config, log *slog.Logger, noPay bool) error {
	rt, err := newRuntime(cfg, log, noPay)
	if err != nil {
		return err
	}

	proxy := payer.NewProxy(cfg, rt.client, log)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fields := []any{
		"version", version,
		"listen", cfg.Listen,
		"upstream", cfg.Upstream,
		"network", cfg.Network,
	}
	if noPay {
		fields = append(fields, "noPay", true)
	}
	if signer := rt.client.Payer(); signer != nil {
		fields = append(fields, "payer", signer.Address())
	}
	log.Info("x402-celestia-rpc-payer listening", fields...)

	errs := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errs:
		return err
	case <-stop:
		log.Info("stopping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// --- helpers ---------------------------------------------------------------

// buildCall makes the request builder of 1 RPC call.
func buildCall(cfg *payer.Config, method string, params map[string]string, jsonRPC bool) payer.RequestFunc {
	base := strings.TrimRight(cfg.Upstream, "/")

	if jsonRPC {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  method,
			"params":  params,
		})
		return func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(string(body)))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		}
	}

	query := url.Values{}
	for key, value := range params {
		query.Set(key, value)
	}
	target := base + "/" + method
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, target, nil)
	}
}

// parseParams reads the "key=value" arguments of a call.
//
// NOTE: The URI style of CometBFT needs quotation marks around a string value.
// parseParams adds them when the value is not a number, not a boolean, and not
// a 0x hash.
func parseParams(args []string) (map[string]string, error) {
	params := make(map[string]string, len(args))
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("the argument %q is not a \"key=value\" pair", arg)
		}
		params[key] = quoteParam(value)
	}
	return params, nil
}

// quoteParam puts quotation marks around a string value.
func quoteParam(value string) string {
	if value == "" {
		return `""`
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value
	}
	if value == "true" || value == "false" {
		return value
	}
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		return value
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return value
	}
	return `"` + value + `"`
}

// printJSON prints a body. It indents the body when the body is JSON.
func printJSON(body []byte) {
	var pretty json.RawMessage
	if err := json.Unmarshal(body, &pretty); err == nil {
		out, err := json.MarshalIndent(pretty, "", "  ")
		if err == nil {
			fmt.Println(string(out))
			return
		}
	}
	fmt.Println(strings.TrimRight(string(body), "\n"))
}
