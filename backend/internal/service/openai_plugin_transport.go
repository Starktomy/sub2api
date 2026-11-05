package service

import "net/http"

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if supplierAccountMarked(account) {
		dispatch := supplierDispatchFromContext(request.Context())
		if dispatch == nil || s.pluginManager.ShouldRouteOpenAIOAuth(account) {
			return nil, ErrSupplierDispatchUnavailable
		}
		if err := dispatch.beforeSend(request.Context(), account); err != nil {
			return nil, err
		}
		request = request.Clone(WithHTTPUpstreamRedirectsDisabled(request.Context()))
		// Disable transport replay of a non-empty POST body, including HTTP/2
		// retries. Redirect policy is independent and also explicitly disabled.
		request.GetBody = nil
		request.Header.Del("Idempotency-Key")
		request.Header.Del("X-Idempotency-Key")
		resp, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
		if err != nil {
			return resp, &SupplierAccountingError{}
		}
		return resp, nil
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
