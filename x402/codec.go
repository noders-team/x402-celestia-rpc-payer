package x402

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// EncodeHeader encodes v as JSON, then as base64url. The result is the value
// of an x402 header.
func EncodeHeader(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// DecodeHeader decodes an x402 header value into dst.
//
// It reads base64url first, and standard base64 after it. Both encodings are
// in use, and the 2 differ in 2 characters only.
func DecodeHeader(raw string, dst any) error {
	b, err := decodeBase64(raw)
	if err != nil {
		return fmt.Errorf("base64 decode: %w", err)
	}
	return json.Unmarshal(b, dst)
}

// ParsePaymentPayload reads the value of the Payment-Signature header.
//
// A v1 payload carries the scheme and the network at the top level. This
// function moves them into the Accepted field, so a caller reads the v2 shape
// only.
func ParsePaymentPayload(raw string) (PaymentPayloadV2, error) {
	b, err := decodeBase64(raw)
	if err != nil {
		return PaymentPayloadV2{}, fmt.Errorf("base64 decode payment-signature: %w", err)
	}

	version, err := peekVersion(b)
	if err != nil {
		return PaymentPayloadV2{}, err
	}

	if version == VersionV1 {
		var v1 PaymentPayloadV1
		if err := json.Unmarshal(b, &v1); err != nil {
			return PaymentPayloadV2{}, fmt.Errorf("parse v1 payload: %w", err)
		}
		return PaymentPayloadV2{
			X402Version: VersionV2,
			Accepted:    PaymentOption{Scheme: v1.Scheme, Network: v1.Network},
			Payload:     v1.Payload,
		}, nil
	}

	var v2 PaymentPayloadV2
	if err := json.Unmarshal(b, &v2); err != nil {
		return PaymentPayloadV2{}, fmt.Errorf("parse v2 payload: %w", err)
	}
	return v2, nil
}

// ParsePaymentRequired reads the value of the Payment-Required header.
//
// The value is base64, or it is the plain JSON body of the 402 answer. This
// function takes both, and it moves a v1 answer into the v2 shape.
func ParsePaymentRequired(raw string) (PaymentRequiredV2, error) {
	b, err := decodeBase64(raw)
	if err != nil {
		// The 402 body of a v1 server is plain JSON, not base64.
		b = []byte(raw)
	}

	version, err := peekVersion(b)
	if err != nil {
		return PaymentRequiredV2{}, err
	}

	if version == VersionV1 {
		var v1 PaymentRequiredV1
		if err := json.Unmarshal(b, &v1); err != nil {
			return PaymentRequiredV2{}, fmt.Errorf("parse v1 payment-required: %w", err)
		}
		return PaymentRequiredV2{X402Version: VersionV2, Accepts: v1.Accepts}, nil
	}

	var v2 PaymentRequiredV2
	if err := json.Unmarshal(b, &v2); err != nil {
		return PaymentRequiredV2{}, fmt.Errorf("parse v2 payment-required: %w", err)
	}
	return v2, nil
}

// decodeBase64 reads base64url, then standard base64.
func decodeBase64(raw string) ([]byte, error) {
	if b, err := base64.URLEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(raw)
}

// peekVersion reads the x402Version field of a JSON object.
func peekVersion(b []byte) (int, error) {
	var peek struct {
		X402Version int `json:"x402Version"`
	}
	if err := json.Unmarshal(b, &peek); err != nil {
		return 0, fmt.Errorf("parse version: %w", err)
	}
	return peek.X402Version, nil
}
