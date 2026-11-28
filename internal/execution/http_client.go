package execution

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// BatchQuoteClient is an optional VenueClient capability for fetching multiple
// top-of-book quotes in a single batched round-trip or bounded fan-out.
type BatchQuoteClient interface {
	GetQuotes(ctx context.Context, symbols []string) (map[string]Quote, error)
}

// NewOptimizedHTTPClient constructs an *http.Client tuned for low-latency venue API calls
// with persistent HTTP/2 multiplexing, TLS session resumption, and 32 KiB socket buffers.
func NewOptimizedHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
			}
			return conn, nil
		},
		ForceAttemptHTTP2: true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		MaxConnsPerHost:       16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		WriteBufferSize:       32 << 10,
		ReadBufferSize:        32 << 10,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ClientSessionCache: tls.NewLRUClientSessionCache(64),
		},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}
