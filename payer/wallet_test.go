package payer

import (
	"strings"
	"testing"

	"github.com/cosmos/go-bip39"
)

// testAddress is the address of testMnemonic at the default path.
// cosmjs gives the same address for the same mnemonic and the same path.
const testAddress = "celestia19rl4cm2hmr8afy4kldpxz3fka4jguq0ad2ud9c"

func TestNewWalletAddress(t *testing.T) {
	wallet, err := NewWallet(testMnemonic, "m/44'/118'/0'/0/0", "celestia")
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	if wallet.Address() != testAddress {
		t.Errorf("address = %q, want %q", wallet.Address(), testAddress)
	}
}

func TestNewWalletPathChangesTheAddress(t *testing.T) {
	first, err := NewWallet(testMnemonic, "m/44'/118'/0'/0/0", "celestia")
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	second, err := NewWallet(testMnemonic, "m/44'/118'/0'/0/1", "celestia")
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	if first.Address() == second.Address() {
		t.Errorf("2 HD paths gave the same address %q", first.Address())
	}
}

func TestNewWalletPrefix(t *testing.T) {
	wallet, err := NewWallet(testMnemonic, "m/44'/118'/0'/0/0", "cosmos")
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	if !strings.HasPrefix(wallet.Address(), "cosmos1") {
		t.Errorf("address = %q, want a cosmos1… address", wallet.Address())
	}
}

func TestNewWalletRejects(t *testing.T) {
	tests := []struct {
		name     string
		mnemonic string
		path     string
		prefix   string
		reason   string
	}{
		{"bad mnemonic", "not a mnemonic", "m/44'/118'/0'/0/0", "celestia", "not a valid BIP-39"},
		{"empty path", testMnemonic, "", "celestia", "HD path is empty"},
		{"empty prefix", testMnemonic, "m/44'/118'/0'/0/0", "", "prefix is empty"},
		{"bad path", testMnemonic, "not/a/path", "celestia", "derive key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWallet(tt.mnemonic, tt.path, tt.prefix)
			if err == nil {
				t.Fatalf("the wallet was built, but it must fail")
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error = %q, want it to hold %q", err, tt.reason)
			}
		})
	}
}

func TestNewMnemonicHas24Words(t *testing.T) {
	mnemonic, err := NewMnemonic()
	if err != nil {
		t.Fatalf("new mnemonic: %v", err)
	}
	if words := len(strings.Fields(mnemonic)); words != 24 {
		t.Errorf("the mnemonic has %d words, want 24", words)
	}
	if !bip39.IsMnemonicValid(mnemonic) {
		t.Errorf("the mnemonic is not valid: %q", mnemonic)
	}
	if _, err := NewWallet(mnemonic, "m/44'/118'/0'/0/0", "celestia"); err != nil {
		t.Errorf("the new mnemonic does not build a wallet: %v", err)
	}
}
