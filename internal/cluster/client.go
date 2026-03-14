package cluster

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// PublicAuthInjector applies a peer public API key to an outbound request.
type PublicAuthInjector func(http.Header, string)

// DoPublicRequest sends a request to the peer's public API using the configured key order.
// Unauthorized or forbidden responses trigger a fallback to the next configured key.
func (s *Service) DoPublicRequest(ctx context.Context, binding *PeerBinding, method, requestPath, rawQuery string, headers http.Header, body []byte, inject PublicAuthInjector) (*http.Response, string, error) {
	if s == nil {
		return nil, "", ErrClusterDisabled
	}
	if binding == nil || binding.ConfiguredID == "" || binding.AdvertiseURL == "" {
		return nil, "", fmt.Errorf("cluster peer binding is incomplete")
	}
	peer, err := s.ResolvePeer(binding.ConfiguredID)
	if err != nil {
		return nil, "", err
	}

	s.mu.RLock()
	timeout := s.cfg.ForwardTimeout
	client := s.newHTTPClientLocked(timeout)
	order := s.apiKeyOrder(peer)
	s.mu.RUnlock()

	if len(order) == 0 {
		return nil, "", fmt.Errorf("cluster peer %s has no public api keys", binding.ConfiguredID)
	}

	targetURL, err := joinURL(binding.AdvertiseURL, requestPath, stripPublicAuthQuery(rawQuery))
	if err != nil {
		return nil, "", err
	}

	var lastAuthResp *http.Response
	for _, apiKey := range order {
		req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
		if err != nil {
			if lastAuthResp != nil {
				_ = lastAuthResp.Body.Close()
			}
			return nil, "", err
		}
		copyForwardHeaders(req.Header, headers)
		if inject != nil {
			inject(req.Header, apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			if lastAuthResp != nil {
				_ = lastAuthResp.Body.Close()
			}
			return nil, "", err
		}

		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			if lastAuthResp != nil {
				_ = lastAuthResp.Body.Close()
			}
			lastAuthResp = resp
			continue
		default:
			s.setPreferredAPIKey(binding.ConfiguredID, apiKey)
			if lastAuthResp != nil {
				_ = lastAuthResp.Body.Close()
			}
			return resp, apiKey, nil
		}
	}

	s.setPreferredAPIKey(binding.ConfiguredID, "")
	if lastAuthResp != nil {
		return lastAuthResp, "", nil
	}
	return nil, "", fmt.Errorf("cluster peer %s has no usable public api keys", binding.ConfiguredID)
}

func stripPublicAuthQuery(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	values.Del("key")
	values.Del("auth_token")
	return values.Encode()
}
