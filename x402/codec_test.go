package x402

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeHeaderAndDecodeHeader(t *testing.T) {
	want := PaymentResponse{
		Status:          "success",
		TransactionHash: "ABCD",
		Network:         "cosmos:mocha-5",
		Payer:           "celestia1test",
	}

	raw, err := EncodeHeader(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var got PaymentResponse
	if err := DecodeHeader(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestDecodeHeaderTakesStandardBase64(t *testing.T) {
	// A "+" and a "/" separate standard base64 from base64url. This payer
	// must read a header of a server that uses the standard encoding.
	body := `{"status":"success","transactionHash":"a+b/c=="}`
	raw := base64.StdEncoding.EncodeToString([]byte(body))

	var got PaymentResponse
	if err := DecodeHeader(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TransactionHash != "a+b/c==" {
		t.Errorf("transactionHash = %q", got.TransactionHash)
	}
}

func TestDecodeHeaderRejectsTextThatIsNotBase64(t *testing.T) {
	var got PaymentResponse
	err := DecodeHeader("not base64 at all!", &got)
	if err == nil {
		t.Fatalf("the value was accepted, but it is not base64")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("error = %q, want it to name base64", err)
	}
}

func TestParsePaymentRequiredReadsAHeader(t *testing.T) {
	want := PaymentRequiredV2{
		X402Version: VersionV2,
		Accepts: []PaymentOption{{
			Scheme:  SchemeExact,
			Network: "cosmos:mocha-5",
			Amount:  "1000",
			Asset:   "utia",
			PayTo:   "celestia1test",
		}},
	}
	raw, err := EncodeHeader(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := ParsePaymentRequired(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Accepts) != 1 {
		t.Fatalf("accepts = %d, want 1", len(got.Accepts))
	}
	if !reflect.DeepEqual(got.Accepts[0], want.Accepts[0]) {
		t.Errorf("option = %+v, want %+v", got.Accepts[0], want.Accepts[0])
	}
}

func TestParsePaymentRequiredReadsPlainJSON(t *testing.T) {
	// A v1 server puts the plain object in the body of the 402 answer.
	body := `{"x402Version":1,"accepts":[{"scheme":"exact","network":"cosmos:mocha-5",
		"amount":"1000","asset":"utia","payTo":"celestia1test"}]}`

	got, err := ParsePaymentRequired(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.X402Version != VersionV2 {
		t.Errorf("version = %d, want %d", got.X402Version, VersionV2)
	}
	if len(got.Accepts) != 1 || got.Accepts[0].Amount != "1000" {
		t.Errorf("accepts = %+v", got.Accepts)
	}
}

func TestParsePaymentRequiredRejectsABodyThatIsNotJSON(t *testing.T) {
	if _, err := ParsePaymentRequired("<html>502</html>"); err == nil {
		t.Fatalf("the body was accepted, but it is not JSON")
	}
}

func TestParsePaymentPayloadMovesAV1PayloadIntoTheV2Shape(t *testing.T) {
	v1 := PaymentPayloadV1{
		X402Version: VersionV1,
		Scheme:      SchemeExact,
		Network:     "cosmos:mocha-5",
		Payload:     json.RawMessage(`{"signedTx":"AAAA"}`),
	}
	raw, err := EncodeHeader(v1)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := ParsePaymentPayload(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.X402Version != VersionV2 {
		t.Errorf("version = %d, want %d", got.X402Version, VersionV2)
	}
	if got.Accepted.Scheme != SchemeExact {
		t.Errorf("scheme = %q, want %q", got.Accepted.Scheme, SchemeExact)
	}
	if got.Accepted.Network != "cosmos:mocha-5" {
		t.Errorf("network = %q", got.Accepted.Network)
	}

	var payload CosmosPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("parse the payload: %v", err)
	}
	if payload.SignedTx != "AAAA" {
		t.Errorf("signedTx = %q", payload.SignedTx)
	}
}

func TestParsePaymentPayloadReadsAV2Payload(t *testing.T) {
	want := PaymentPayloadV2{
		X402Version: VersionV2,
		Accepted: PaymentOption{
			Scheme:  SchemeUpto,
			Network: "cosmos:celestia",
			Amount:  "2000",
			Asset:   "utia",
			PayTo:   "celestia1test",
		},
		Payload: json.RawMessage(`{"signedTx":"BBBB"}`),
	}
	raw, err := EncodeHeader(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := ParsePaymentPayload(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(got.Accepted, want.Accepted) {
		t.Errorf("accepted = %+v, want %+v", got.Accepted, want.Accepted)
	}
}

func TestParsePaymentPayloadRejectsTextThatIsNotBase64(t *testing.T) {
	_, err := ParsePaymentPayload("not base64 at all!")
	if err == nil {
		t.Fatalf("the value was accepted, but it is not base64")
	}
	if !strings.Contains(err.Error(), "payment-signature") {
		t.Errorf("error = %q, want it to name the header", err)
	}
}
