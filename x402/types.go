// Package x402 holds the wire format of the x402 payment protocol.
//
// The package carries the parts that a Cosmos payer needs: the 3 HTTP header
// names, the payment option of a 402 answer, the payload that the payer sends
// back, and the settlement answer of the server. It holds no chain code and no
// verifier, so it builds with the standard library alone.
//
// The types come from the x402-go library, which is MIT-licensed. See the
// NOTICE section of LICENSE.
//
// # The 3 headers
//
//	Payment-Required    the server asks for a payment, with HTTP 402
//	Payment-Signature   the client sends the signed payment
//	Payment-Response    the server reports the settlement
//
// Each header holds a JSON object in base64url. EncodeHeader and DecodeHeader
// do that step.
package x402

import "encoding/json"

// The versions of the protocol. A v1 message carries the scheme and the
// network at the top level. ParsePaymentRequired and ParsePaymentPayload move
// those 2 fields into the v2 shape, so the rest of the code reads v2 only.
const (
	VersionV1 = 1
	VersionV2 = 2
)

// Scheme is the payment scheme of an option.
type Scheme string

const (
	// SchemeExact asks for the exact amount of the option.
	SchemeExact Scheme = "exact"
	// SchemeUpto asks for an amount up to the amount of the option.
	SchemeUpto Scheme = "upto"
)

// The HTTP header names of the protocol.
const (
	HeaderPaymentRequired  = "Payment-Required"
	HeaderPaymentSignature = "Payment-Signature"
	HeaderPaymentResponse  = "Payment-Response"
)

// Resource describes the endpoint that the caller asks for.
type Resource struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// PaymentOption is 1 payment that a server accepts for a call.
//
// CAUTION: A client must check Asset, Network, PayTo and Amount against its
// own limits before it signs. The server writes every field of this structure.
type PaymentOption struct {
	// Scheme is "exact" or "upto".
	Scheme Scheme `json:"scheme"`
	// Network is the CAIP-2 identifier, for example "cosmos:mocha-5".
	Network string `json:"network"`
	// Amount is the price in the atomic unit of Asset.
	Amount string `json:"amount"`
	// Asset is the denom on a Cosmos chain, or the token address on an
	// EVM chain.
	Asset string `json:"asset"`
	// PayTo is the address that gets the money.
	PayTo string `json:"payTo"`
	// MaxTimeoutSeconds is how long the server holds the option open.
	MaxTimeoutSeconds int `json:"maxTimeoutSeconds"`

	Description string          `json:"description,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	Extra       json.RawMessage `json:"extra,omitempty"`
}

// PaymentRequiredV2 is the body and the header of an HTTP 402 answer.
type PaymentRequiredV2 struct {
	X402Version int             `json:"x402Version"`
	Error       string          `json:"error,omitempty"`
	Resource    *Resource       `json:"resource,omitempty"`
	Accepts     []PaymentOption `json:"accepts"`
}

// PaymentRequiredV1 is the 402 answer of protocol v1.
type PaymentRequiredV1 struct {
	X402Version int             `json:"x402Version"`
	Accepts     []PaymentOption `json:"accepts"`
}

// CosmosAuthorization names what a Cosmos payment pays.
//
// The server reads the same values from the signed transaction, so this
// structure is a summary. It is not the authority.
type CosmosAuthorization struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount string `json:"amount"`
	Denom  string `json:"denom"`
	// TimeoutAt is a Unix timestamp in seconds.
	TimeoutAt int64 `json:"timeoutAt"`
}

// CosmosPayload is the payload of a payment on a Cosmos chain.
type CosmosPayload struct {
	// Signature is empty for a Cosmos payment. SignedTx carries the
	// signature of the transaction.
	//
	// CAUTION: The tag has no "omitempty". A server that reads the field
	// must find it, so the wire format keeps the empty value.
	Signature string `json:"signature"`
	// Authorization is a summary of the payment.
	Authorization CosmosAuthorization `json:"authorization"`
	// SignedTx is the base64 of a signed protobuf TxRaw.
	SignedTx string `json:"signedTx"`
}

// PaymentPayloadV2 is the value of the Payment-Signature header.
//
// Accepted holds the complete payment option that the client took. Payload
// holds the payment of the chain: a CosmosPayload for a Cosmos chain.
type PaymentPayloadV2 struct {
	X402Version int             `json:"x402Version"`
	Resource    *Resource       `json:"resource,omitempty"`
	Accepted    PaymentOption   `json:"accepted"`
	Payload     json.RawMessage `json:"payload"`
}

// PaymentPayloadV1 is the Payment-Signature header of protocol v1.
type PaymentPayloadV1 struct {
	X402Version int             `json:"x402Version"`
	Scheme      Scheme          `json:"scheme"`
	Network     string          `json:"network"`
	Payload     json.RawMessage `json:"payload"`
}

// PaymentResponse is the value of the Payment-Response header.
type PaymentResponse struct {
	// Status is "success" or "failed".
	Status string `json:"status"`
	// TransactionHash is the hash of the transaction that the server
	// broadcast. It is present after a success.
	TransactionHash string `json:"transactionHash,omitempty"`
	Network         string `json:"network,omitempty"`
	Payer           string `json:"payer,omitempty"`
	// Error says why the settlement failed.
	Error string `json:"error,omitempty"`
}
