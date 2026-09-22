package payer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/noders-team/x402-celestia-rpc-payer/x402"
)

// maxRequiredBody limits the 402 answer that the payer reads into memory.
const maxRequiredBody = 1 << 20 // 1 MB

// RequestFunc builds a new request. The payer calls it 1 time for the first
// try, and 1 more time for the try that carries the payment. A new request is
// needed each time, because the body of a request can be read only 1 time.
type RequestFunc func() (*http.Request, error)

// Receipt tells what the payer paid for 1 call.
type Receipt struct {
	// Paid is true when the payer signed and sent a payment.
	Paid bool
	// Amount is the price in the atomic unit of the asset.
	Amount string
	// Asset is the denom of the payment.
	Asset string
	// Response is the Payment-Response header of the sidecar. It holds the
	// transaction hash. It is nil when the sidecar sent no header.
	Response *x402.PaymentResponse
}

// Client sends a request to the x402 sidecar and pays for it.
//
// NOTE: The Client holds a lock while it pays. 2 paid calls do not run at the
// same time, because 2 payments must not use the same account sequence.
type Client struct {
	cfg   *Config
	http  *http.Client
	payer *Payer
	log   *slog.Logger

	mu sync.Mutex
}

// NewClient builds a Client. The payer may be nil. Do then gives every 402
// answer back, and it signs nothing.
func NewClient(cfg *Config, payer *Payer, log *slog.Logger) *Client {
	return &Client{
		cfg:   cfg,
		http:  &http.Client{Timeout: 120 * time.Second},
		payer: payer,
		log:   log,
	}
}

// Payer returns the Payer of the Client. It is nil when the payer has no
// wallet.
func (c *Client) Payer() *Payer { return c.payer }

// Do sends the request to the sidecar.
//
// If the sidecar answers 402, Do signs a payment and sends the request 1 more
// time with the Payment-Signature header. If the sidecar answers 402 again, Do
// gives that answer back. Do does not send a third request.
//
// The caller must close the body of the answer.
func (c *Client) Do(ctx context.Context, build RequestFunc) (*http.Response, *Receipt, error) {
	res, err := c.send(ctx, build, "")
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode != http.StatusPaymentRequired {
		return res, &Receipt{}, nil
	}

	// The sidecar asks for a payment. Read the answer into memory, so that the
	// payer can give it back when it does not pay.
	body, err := io.ReadAll(io.LimitReader(res.Body, maxRequiredBody))
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))

	if err != nil {
		return res, &Receipt{}, nil
	}
	if c.payer == nil {
		c.log.Warn("the sidecar asks for a payment, but the payer has no wallet")
		return res, &Receipt{}, nil
	}

	required, err := readPaymentRequired(res.Header, body)
	if err != nil {
		c.log.Error("the 402 answer has no payment option", "err", err)
		return res, &Receipt{}, nil
	}
	option, err := pickOption(required, c.cfg.Network)
	if err != nil {
		c.log.Error("no usable payment option", "err", err)
		return res, &Receipt{}, nil
	}

	// 1 payment at a time. The account sequence does not allow 2.
	c.mu.Lock()
	defer c.mu.Unlock()

	pay, err := c.payer.Pay(ctx, option)
	if err != nil {
		c.log.Error("the payer did not pay", "err", err, "amount", option.Amount, "asset", option.Asset)
		return res, &Receipt{}, nil
	}
	_ = res.Body.Close()

	c.log.Info("paying", "amount", pay.Amount, "asset", pay.Asset,
		"payTo", option.PayTo, "sequence", pay.Sequence)

	paid, err := c.send(ctx, build, pay.Header)
	if err != nil {
		// The answer did not arrive. The sidecar may have broadcast the
		// transaction, so the payer must not take the payment back here.
		// The Payer asks the chain about it on the next payment.
		c.payer.Unresolved(pay)
		c.log.Error("the answer to a payment did not arrive. The payer reads the chain on the next payment.",
			"err", err, "tx", pay.TxHash, "amount", pay.Amount, "asset", pay.Asset)
		return nil, nil, err
	}

	receipt := &Receipt{
		Paid:     true,
		Amount:   pay.Amount.String(),
		Asset:    pay.Asset,
		Response: readPaymentResponse(paid.Header),
	}

	if !paymentWorked(paid.StatusCode, receipt.Response) {
		c.payer.Rollback(pay)
		receipt.Paid = false
		c.log.Error("the sidecar did not accept the payment",
			"status", paid.StatusCode, "err", paymentError(receipt.Response))
		return paid, receipt, nil
	}

	if receipt.Response != nil && receipt.Response.TransactionHash != "" {
		c.log.Info("paid", "amount", pay.Amount, "asset", pay.Asset,
			"tx", receipt.Response.TransactionHash, "status", receipt.Response.Status)
	}
	return paid, receipt, nil
}

// send builds a request and sends it. A header that is not empty goes into the
// Payment-Signature header.
func (c *Client) send(ctx context.Context, build RequestFunc, payment string) (*http.Response, error) {
	req, err := build()
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	if payment != "" {
		req.Header.Set(x402.HeaderPaymentSignature, payment)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the sidecar at %s did not answer: %w", c.cfg.Upstream, err)
	}
	return res, nil
}

// paymentWorked reports if the sidecar took the payment.
func paymentWorked(status int, pr *x402.PaymentResponse) bool {
	if status == http.StatusPaymentRequired {
		return false
	}
	if pr != nil && pr.Status == "failed" {
		return false
	}
	return true
}

// paymentError returns the error of a payment response.
func paymentError(pr *x402.PaymentResponse) string {
	if pr == nil {
		return "the sidecar sent no Payment-Response header"
	}
	if pr.Error == "" {
		return pr.Status
	}
	return pr.Error
}

// readPaymentResponse reads the Payment-Response header of an answer.
func readPaymentResponse(h http.Header) *x402.PaymentResponse {
	raw := h.Get(x402.HeaderPaymentResponse)
	if raw == "" {
		return nil
	}
	var pr x402.PaymentResponse
	if err := x402.DecodeHeader(raw, &pr); err != nil {
		return nil
	}
	return &pr
}

// readPaymentRequired finds the payment options of a 402 answer.
//
// The sidecar puts them in the Payment-Required header. It also puts them in
// the body: a URI-style call gets the plain object, and a JSON-RPC call gets
// the object in the "data" field of the error.
func readPaymentRequired(h http.Header, body []byte) (x402.PaymentRequiredV2, error) {
	if raw := h.Get(x402.HeaderPaymentRequired); raw != "" {
		if required, err := x402.ParsePaymentRequired(raw); err == nil && len(required.Accepts) > 0 {
			return required, nil
		}
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return x402.PaymentRequiredV2{}, fmt.Errorf("the 402 answer has no Payment-Required header and no body")
	}

	var plain x402.PaymentRequiredV2
	if err := json.Unmarshal(trimmed, &plain); err == nil && len(plain.Accepts) > 0 {
		return plain, nil
	}

	var rpc struct {
		Error struct {
			Message string                 `json:"message"`
			Data    x402.PaymentRequiredV2 `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &rpc); err == nil && len(rpc.Error.Data.Accepts) > 0 {
		return rpc.Error.Data, nil
	}

	return x402.PaymentRequiredV2{}, fmt.Errorf("the 402 answer has no payment option")
}

// pickOption returns the option of the configured network.
func pickOption(required x402.PaymentRequiredV2, network string) (x402.PaymentOption, error) {
	names := make([]string, 0, len(required.Accepts))
	for _, option := range required.Accepts {
		if option.Network == network {
			return option, nil
		}
		names = append(names, option.Network)
	}
	if len(names) == 0 {
		return x402.PaymentOption{}, fmt.Errorf("the 402 answer holds no payment option")
	}
	return x402.PaymentOption{}, fmt.Errorf(
		"the sidecar accepts %s, but the payer pays on %s", strings.Join(names, ", "), network)
}
