package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testMnemonic is a well-known BIP-39 test mnemonic. It holds no money.
const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// testAddress is the address of testMnemonic at the path of the payer.
// cosmjs gives the same address for the same mnemonic and the same path.
const testAddress = "celestia19rl4cm2hmr8afy4kldpxz3fka4jguq0ad2ud9c"

// minimalConfig holds the 3 fields that a test needs. Every other field takes
// its default.
const minimalConfig = `
upstream: "http://localhost:26658"
network: "cosmos:mocha-5"
mnemonic: "` + testMnemonic + `"
`

// writeConfig writes a configuration file in a new directory, and it returns
// the path of that file.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
