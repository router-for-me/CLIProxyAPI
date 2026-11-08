package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"path"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	log "github.com/sirupsen/logrus"
)

// RefreshClaim requires S3 conditional creation, not a last-writer-wins upload.
// Backends that ignore If-None-Match fail the capability probe before any OAuth
// request. A claim is never deleted or retried after an uncertain PUT response.
func (s *ObjectTokenStore) RefreshClaim(ctx context.Context, key string) (bool, error) {
	if s == nil || s.client == nil {
		return false, fmt.Errorf("object refresh journal is unavailable")
	}
	if errProbe := s.ensureRefreshConditionalCreate(ctx); errProbe != nil {
		return false, errProbe
	}
	return s.createRefreshObject(ctx, s.refreshObjectKey(key, "claim"), []byte("pending"))
}

func (s *ObjectTokenStore) RefreshRead(ctx context.Context, key string) ([]byte, error) {
	object, errGet := s.client.GetObject(ctx, s.cfg.Bucket, s.refreshObjectKey(key, "receipt"), minio.GetObjectOptions{})
	if errGet != nil {
		return nil, errGet
	}
	data, errRead := io.ReadAll(object)
	errClose := object.Close()
	if errRead != nil {
		response := minio.ToErrorResponse(errRead)
		if response.StatusCode == http.StatusNotFound {
			_, errStat := s.client.StatObject(ctx, s.cfg.Bucket, s.refreshObjectKey(key, "claim"), minio.StatObjectOptions{})
			if errStat == nil {
				return []byte(codex.PendingRefreshReceipt), nil
			}
			if minio.ToErrorResponse(errStat).StatusCode == http.StatusNotFound {
				return nil, nil
			}
			return nil, errStat
		}
		return nil, errRead
	}
	return data, errClose
}

func (s *ObjectTokenStore) RefreshComplete(ctx context.Context, key string, receipt []byte) error {
	created, errCreate := s.createRefreshObject(ctx, s.refreshObjectKey(key, "receipt"), receipt)
	if errCreate != nil {
		return errCreate
	}
	if !created {
		return fmt.Errorf("refresh receipt already exists")
	}
	return nil
}

func (s *ObjectTokenStore) refreshObjectKey(key, kind string) string {
	return path.Join(s.cfg.Prefix, "codex-refresh", key, kind)
}

func (s *ObjectTokenStore) createRefreshObject(ctx context.Context, key string, data []byte) (bool, error) {
	headers := http.Header{"If-None-Match": []string{"*"}}
	// The bundled minio PutObjectOptions quotes "*" as an ETag; use signed
	// headers to send the actual HTTP wildcard, not the literal ETag "*".
	url, errPresign := s.client.PresignHeader(ctx, http.MethodPut, s.cfg.Bucket, key, time.Minute, nil, headers)
	if errPresign != nil {
		return false, fmt.Errorf("sign conditional refresh journal write: %w", errPresign)
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPut, url.String(), bytes.NewReader(data))
	if errRequest != nil {
		return false, fmt.Errorf("create conditional refresh journal request")
	}
	req.Header = headers
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return false, fmt.Errorf("conditional refresh journal write outcome unknown")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("close refresh journal response: %v", errClose)
		}
	}()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("conditional refresh journal write returned HTTP %d", resp.StatusCode)
	}
	return true, nil
}

func (s *ObjectTokenStore) ensureRefreshConditionalCreate(ctx context.Context) error {
	s.refreshProbeMu.Lock()
	defer s.refreshProbeMu.Unlock()
	if s.refreshProbeReady {
		return nil
	}
	// Cache only positive evidence. A temporary storage outage must not
	// permanently reject new authorizations after the backend recovers.
	if errProbe := s.probeRefreshConditionalCreate(ctx); errProbe != nil {
		return errProbe
	}
	s.refreshProbeReady = true
	return nil
}

func (s *ObjectTokenStore) probeRefreshConditionalCreate(ctx context.Context) error {
	nonce := make([]byte, 32)
	if _, errRand := rand.Read(nonce); errRand != nil {
		return errRand
	}
	key := s.refreshObjectKey(fmt.Sprintf("probe-%x", nonce), "claim")
	first, errFirst := s.createRefreshObject(ctx, key, []byte("probe"))
	if errFirst != nil || !first {
		return fmt.Errorf("refresh journal conditional-create probe failed: %v", errFirst)
	}
	second, errSecond := s.createRefreshObject(ctx, key, []byte("probe"))
	if errSecond != nil {
		return errSecond
	}
	if second {
		return fmt.Errorf("object backend does not enforce conditional creation; refresh refused")
	}
	if errRemove := s.client.RemoveObject(ctx, s.cfg.Bucket, key, minio.RemoveObjectOptions{}); errRemove != nil {
		log.Warnf("remove refresh journal capability probe: %v", errRemove)
	}
	return nil
}
