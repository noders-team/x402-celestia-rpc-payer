package payer

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// maxBodyBytes limits the body of a request that the proxy reads.
const maxBodyBytes = 4 << 20 // 4 MB

// hopByHopHeaders do not go to the next server.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// Proxy is a local CometBFT RPC endpoint that pays for each call.
//
// A tool connects to the proxy in the same way that it connects to a node. The
// proxy sends the call to the x402 sidecar. When the sidecar asks for a
// payment, the proxy signs one and sends the call again.
type Proxy struct {
	cfg    *Config
	client *Client
	log    *slog.Logger
}

// NewProxy builds the local proxy.
func NewProxy(cfg *Config, client *Client, log *slog.Logger) *Proxy {
	return &Proxy{cfg: cfg, client: client, log: log}
}

// ServeHTTP handles 1 call.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The proxy owns the /x402/payer/ prefix. Every other path goes to the
	// sidecar, and /x402/health and /x402/prices reach the sidecar itself.
	if p.serveOwnRoutes(w, r) {
		return
	}

	if r.URL.Path == "/websocket" {
		writeError(w, http.StatusNotImplemented,
			"the proxy does not carry /websocket. A websocket cannot pay.")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read the body: "+err.Error())
		return
	}

	target := p.targetURL(r.URL)
	build := func() (*http.Request, error) {
		req, err := http.NewRequest(r.Method, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		copyHeader(req.Header, r.Header)
		return req, nil
	}

	res, receipt, err := p.client.Do(r.Context(), build)
	if err != nil {
		p.log.Error("the call failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer res.Body.Close()

	if receipt.Paid {
		p.log.Info("paid call", "path", r.URL.Path, "amount", receipt.Amount, "asset", receipt.Asset)
	}

	copyHeader(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	if _, err := io.Copy(w, res.Body); err != nil {
		p.log.Error("could not send the answer", "err", err)
	}
}

// serveOwnRoutes answers the routes of the proxy itself.
// It returns true when it answered.
func (p *Proxy) serveOwnRoutes(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/x402/payer") {
		return false
	}

	switch r.URL.Path {
	case "/x402/payer/health":
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"upstream": p.cfg.Upstream,
			"network":  p.cfg.Network,
		})
	case "/x402/payer/status":
		out := map[string]any{
			"upstream":   p.cfg.Upstream,
			"network":    p.cfg.Network,
			"asset":      p.cfg.Asset,
			"maxPerCall": p.cfg.MaxPerCall,
			"budget":     p.cfg.Budget,
		}
		if payer := p.client.Payer(); payer != nil {
			spent, count := payer.Spent()
			out["payer"] = payer.Address()
			out["spent"] = spent.String()
			out["payments"] = count
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, "the proxy has no route "+r.URL.Path)
	}
	return true
}

// targetURL builds the address of the same call on the sidecar.
func (p *Proxy) targetURL(u *url.URL) string {
	target := strings.TrimRight(p.cfg.Upstream, "/") + u.Path
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target
}

// copyHeader copies the headers that go to the next server.
func copyHeader(dst, src http.Header) {
	for name, values := range src {
		if isHopByHop(name) {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func isHopByHop(name string) bool {
	for _, h := range hopByHopHeaders {
		if strings.EqualFold(name, h) {
			return true
		}
	}
	return false
}

// writeError answers with a JSON object that holds 1 "error" field.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
