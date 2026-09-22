package payer

import (
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/cosmos/go-bip39"
)

// mnemonicEntropyBits gives a 24-word mnemonic.
const mnemonicEntropyBits = 256

const (
	// HDPath is the BIP-44 path of the payer wallet. Celestia uses coin
	// type 118. It is fixed, because a customer must not have to know BIP-44.
	// 2 payers need 2 mnemonics, not 2 paths.
	HDPath = "m/44'/118'/0'/0/0"
	// Bech32Prefix is the address prefix of Celestia.
	Bech32Prefix = "celestia"
)

// Wallet holds the key that signs the payments.
//
// WARNING: The private key stays in the memory of the process while it runs.
// Run the payer on a machine that you control.
type Wallet struct {
	priv    *secp256k1.PrivKey
	address string
}

// NewWallet derives the key of a mnemonic at an HD path.
//
// The prefix is the bech32 prefix of the address, "celestia" for Celestia.
// The path is a BIP-44 path, "m/44'/118'/0'/0/0" for a Cosmos chain.
func NewWallet(mnemonic, path, prefix string) (*Wallet, error) {
	mnemonic = strings.TrimSpace(mnemonic)
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("the mnemonic is not a valid BIP-39 mnemonic")
	}
	if path == "" {
		return nil, fmt.Errorf("the HD path is empty")
	}
	if prefix == "" {
		return nil, fmt.Errorf("the bech32 prefix is empty")
	}

	seed := bip39.NewSeed(mnemonic, "")
	master, chainCode := hd.ComputeMastersFromSeed(seed)
	keyBytes, err := hd.DerivePrivateKeyForPath(master, chainCode, path)
	if err != nil {
		return nil, fmt.Errorf("derive key for path %s: %w", path, err)
	}

	priv := &secp256k1.PrivKey{Key: keyBytes}
	address, err := bech32.ConvertAndEncode(prefix, priv.PubKey().Address().Bytes())
	if err != nil {
		return nil, fmt.Errorf("encode address: %w", err)
	}
	return &Wallet{priv: priv, address: address}, nil
}

// WalletFromMnemonic derives the payer wallet of a mnemonic.
//
// It uses the fixed path HDPath and the fixed prefix Bech32Prefix. A customer
// must not have to know BIP-44, so the payer keeps both constant. 2 payers
// need 2 mnemonics, not 2 paths.
func WalletFromMnemonic(mnemonic string) (*Wallet, error) {
	return NewWallet(mnemonic, HDPath, Bech32Prefix)
}

// WalletFromConfig derives the payer wallet of a configuration.
//
// It reads the mnemonic of the configuration file, or of the mnemonic file
// that the configuration names.
func WalletFromConfig(cfg *Config) (*Wallet, error) {
	mnemonic, err := cfg.ReadMnemonic()
	if err != nil {
		return nil, err
	}
	return WalletFromMnemonic(mnemonic)
}

// Address returns the bech32 address of the wallet.
func (w *Wallet) Address() string { return w.address }

// PubKey returns the public key of the wallet.
func (w *Wallet) PubKey() cryptotypes.PubKey { return w.priv.PubKey() }

// Sign signs the bytes with the key of the wallet.
func (w *Wallet) Sign(msg []byte) ([]byte, error) { return w.priv.Sign(msg) }

// NewMnemonic makes a 24-word BIP-39 mnemonic.
func NewMnemonic() (string, error) {
	entropy, err := bip39.NewEntropy(mnemonicEntropyBits)
	if err != nil {
		return "", fmt.Errorf("make entropy: %w", err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", fmt.Errorf("make mnemonic: %w", err)
	}
	return mnemonic, nil
}
