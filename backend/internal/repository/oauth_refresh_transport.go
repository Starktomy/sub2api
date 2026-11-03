package repository

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
	"github.com/imroc/req/v3"
	utls "github.com/refraction-networking/utls"
)

// refreshRequestClient never mutates a shared client. Strict maintenance uses
// fresh HTTP/1 connections: no HTTP/2 replay, reused-connection retry or redirect.
// Legacy clients retain their original pooling, impersonation and retry policy.
func refreshRequestClient(ctx context.Context, proxyURL string, timeout time.Duration, chrome bool, legacy func(string) (*req.Client, error)) (*req.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !refreshpolicy.SingleAttempt(ctx) {
		return legacy(proxyURL)
	}
	trimmed, _, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	client := req.C().SetTimeout(timeout).SetCookieJar(nil)
	if chrome {
		client.ImpersonateChrome()
		// The browser preset advertises h2 independently of ForceHTTP1.
		// Restrict its ClientHello too, or TLS negotiates h2 then sends HTTP/1.
		client.SetTLSHandshake(func(ctx context.Context, addr string, plain net.Conn) (net.Conn, *tls.ConnectionState, error) {
			host := addr
			if parsed, _, err := net.SplitHostPort(addr); err == nil {
				host = parsed
			}
			cfg := client.GetTLSClientConfig()
			conn := utls.UClient(plain, &utls.Config{ServerName: host, RootCAs: cfg.RootCAs, InsecureSkipVerify: cfg.InsecureSkipVerify}, utls.HelloChrome_120)
			if err := conn.BuildHandshakeState(); err != nil {
				return nil, nil, err
			}
			for _, extension := range conn.Extensions {
				switch typed := extension.(type) {
				case *utls.ALPNExtension:
					typed.AlpnProtocols = []string{"http/1.1"}
				case *utls.ApplicationSettingsExtension:
					typed.SupportedProtocols = nil
				case *utls.ApplicationSettingsExtensionNew:
					typed.SupportedProtocols = nil
				}
			}
			if err := conn.HandshakeContext(ctx); err != nil {
				return nil, nil, err
			}
			cs := conn.ConnectionState()
			return conn, &tls.ConnectionState{Version: cs.Version, HandshakeComplete: cs.HandshakeComplete, NegotiatedProtocol: cs.NegotiatedProtocol, ServerName: cs.ServerName, PeerCertificates: cs.PeerCertificates, VerifiedChains: cs.VerifiedChains}, nil
		})
	}
	client.SetCommonRetryCount(0).EnableForceHTTP1().DisableKeepAlives().SetRedirectPolicy(req.NoRedirectPolicy())
	if trimmed != "" {
		client.SetProxyURL(trimmed)
	}
	return instrumentReqClient(client), nil
}
