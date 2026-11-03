package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
	"github.com/imroc/req/v3"
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
	}
	client.SetCommonRetryCount(0).EnableForceHTTP1().DisableKeepAlives().SetRedirectPolicy(req.NoRedirectPolicy())
	if trimmed != "" {
		client.SetProxyURL(trimmed)
	}
	return instrumentReqClient(client), nil
}
