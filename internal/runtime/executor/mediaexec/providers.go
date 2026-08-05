// Package mediaexec implements provider-specific media executors for the
// public sdk/cliproxy/media contracts. Transport/signing live here; durable
// job state does not.
package mediaexec

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/media"
)

// HTTPDoer abstracts http.Client for tests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func defaultDoer() HTTPDoer { return http.DefaultClient }

// --- Higgsfield ---

type Higgsfield struct {
	BaseURL string
	Doer    HTTPDoer
}

func (h *Higgsfield) Identifier() string { return "higgsfield" }
func (h *Higgsfield) Operations() []media.Operation {
	return []media.Operation{media.OpImageToVideo, media.OpVideoGeneration}
}

func (h *Higgsfield) ExecuteMedia(ctx context.Context, req media.Request, opts media.Options) (media.Result, error) {
	base := strings.TrimRight(h.BaseURL, "/")
	if base == "" {
		base = "https://platform.higgsfield.ai"
	}
	doer := h.Doer
	if doer == nil {
		doer = defaultDoer()
	}
	authHeader, err := higgsfieldAuth(opts)
	if err != nil {
		return media.Result{}, err
	}
	switch req.Phase {
	case media.PhaseCreate:
		body := map[string]any{
			"model":  req.Model,
			"prompt": req.Prompt,
		}
		for k, v := range req.Params {
			body[k] = v
		}
		raw, _ := json.Marshal(body)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/image2video/dop", bytes.NewReader(raw))
		httpReq.Header.Set("Authorization", authHeader)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := doer.Do(httpReq)
		res := media.Result{HTTPResponded: resp != nil, SelectedAuth: media.SelectedAuth{Provider: "higgsfield", AuthID: opts.PinnedAuthID}}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		if id, ok := parsed["request_id"].(string); ok {
			res.Handle, _ = json.Marshal(map[string]string{"request_id": id})
			res.AcceptedHandle = true
		}
		if st, ok := parsed["status"].(string); ok {
			res.Status = mapHiggsfieldStatus(st)
		}
		if resp.StatusCode >= 400 {
			res.ErrorCode = fmt.Sprintf("http_%d", resp.StatusCode)
			res.ErrorMessage = "provider error"
		}
		return res, nil
	case media.PhaseStatus:
		var handle struct {
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(req.Handle, &handle)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/requests/"+handle.RequestID+"/status", nil)
		httpReq.Header.Set("Authorization", authHeader)
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "higgsfield", AuthID: opts.PinnedAuthID}, Handle: req.Handle}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		st, _ := parsed["status"].(string)
		res.Status = mapHiggsfieldStatus(st)
		if res.Status == "completed" {
			if video, ok := parsed["video"].(map[string]any); ok {
				if u, ok := video["url"].(string); ok {
					res.Assets = []media.Asset{{RemoteURL: u, MimeType: "video/mp4"}}
				}
			}
		}
		return res, nil
	case media.PhaseCancel:
		var handle struct {
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(req.Handle, &handle)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/requests/"+handle.RequestID+"/cancel", nil)
		httpReq.Header.Set("Authorization", authHeader)
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "higgsfield", AuthID: opts.PinnedAuthID}, Handle: req.Handle}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		return res, nil
	default:
		return media.Result{}, fmt.Errorf("higgsfield: unsupported phase %s", req.Phase)
	}
}

func mapHiggsfieldStatus(st string) string {
	switch st {
	case "queued":
		return "queued"
	case "in_progress":
		return "in_progress"
	case "completed":
		return "completed"
	case "failed", "nsfw":
		return "failed"
	default:
		return "in_progress"
	}
}

func higgsfieldAuth(opts media.Options) (string, error) {
	// Auth material is expected via headers from the auth Manager when used
	// through HttpRequest. For direct mediaexec tests/callers, Options.Headers
	// may carry Authorization already.
	if opts.Headers != nil {
		if v := opts.Headers.Get("Authorization"); v != "" {
			return v, nil
		}
		keyID := opts.Headers.Get("X-Media-Key-ID")
		keySecret := opts.Headers.Get("X-Media-Key-Secret")
		if keyID != "" && keySecret != "" {
			return "Key " + keyID + ":" + keySecret, nil
		}
	}
	return "", fmt.Errorf("higgsfield: missing structured key_id/key_secret credentials")
}

// Ensure Higgsfield implements media.Executor
var _ media.Executor = (*Higgsfield)(nil)

// --- MiniMax Video ---

type MiniMaxVideo struct {
	BaseURL string
	Doer    HTTPDoer
}

func (m *MiniMaxVideo) Identifier() string { return "minimax" }
func (m *MiniMaxVideo) Operations() []media.Operation {
	return []media.Operation{media.OpTextToVideo, media.OpImageToVideo, media.OpVideoGeneration}
}

func (m *MiniMaxVideo) ExecuteMedia(ctx context.Context, req media.Request, opts media.Options) (media.Result, error) {
	base := strings.TrimRight(m.BaseURL, "/")
	if base == "" {
		base = "https://api.minimax.io"
	}
	doer := m.Doer
	if doer == nil {
		doer = defaultDoer()
	}
	authz, err := bearerAuth(opts)
	if err != nil {
		return media.Result{}, err
	}
	switch req.Phase {
	case media.PhaseCreate:
		body := map[string]any{"model": req.Model, "prompt": req.Prompt}
		for k, v := range req.Params {
			body[k] = v
		}
		raw, _ := json.Marshal(body)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/video_generation", bytes.NewReader(raw))
		httpReq.Header.Set("Authorization", authz)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "minimax", AuthID: opts.PinnedAuthID}}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		if br, ok := parsed["base_resp"].(map[string]any); ok {
			if code, ok := br["status_code"].(float64); ok && code != 0 {
				res.ErrorCode = fmt.Sprintf("base_resp_%d", int(code))
				res.ErrorMessage = "provider business error"
				return res, nil
			}
		}
		if tid, ok := parsed["task_id"].(string); ok {
			res.Handle, _ = json.Marshal(map[string]string{"task_id": tid})
			res.AcceptedHandle = true
			res.Status = "queued"
		}
		return res, nil
	case media.PhaseStatus:
		var handle struct {
			TaskID string `json:"task_id"`
			FileID string `json:"file_id"`
		}
		_ = json.Unmarshal(req.Handle, &handle)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/query/video_generation?task_id="+handle.TaskID, nil)
		httpReq.Header.Set("Authorization", authz)
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "minimax", AuthID: opts.PinnedAuthID}, Handle: req.Handle}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		st, _ := parsed["status"].(string)
		res.Status = mapMiniMaxVideoStatus(st)
		if fid, ok := parsed["file_id"].(string); ok && fid != "" {
			handle.FileID = fid
			res.Handle, _ = json.Marshal(handle)
		}
		return res, nil
	case media.PhaseContent:
		var handle struct {
			FileID string `json:"file_id"`
		}
		_ = json.Unmarshal(req.Handle, &handle)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/files/retrieve?file_id="+handle.FileID, nil)
		httpReq.Header.Set("Authorization", authz)
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "minimax", AuthID: opts.PinnedAuthID}, Handle: req.Handle}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		if file, ok := parsed["file"].(map[string]any); ok {
			if u, ok := file["download_url"].(string); ok {
				res.Assets = []media.Asset{{RemoteURL: u, MimeType: "video/mp4"}}
				res.Status = "completed"
			}
		}
		return res, nil
	default:
		return media.Result{}, fmt.Errorf("minimax video: unsupported phase %s", req.Phase)
	}
}

func mapMiniMaxVideoStatus(st string) string {
	switch st {
	case "Preparing", "Queueing":
		return "queued"
	case "Processing":
		return "in_progress"
	case "Success":
		return "completed"
	case "Fail":
		return "failed"
	default:
		return "in_progress"
	}
}

var _ media.Executor = (*MiniMaxVideo)(nil)

// --- MiniMax Music ---

type MiniMaxMusic struct {
	BaseURL string
	Doer    HTTPDoer
}

func (m *MiniMaxMusic) Identifier() string { return "minimax-music" }
func (m *MiniMaxMusic) Operations() []media.Operation {
	return []media.Operation{media.OpMusicGeneration}
}

func (m *MiniMaxMusic) ExecuteMedia(ctx context.Context, req media.Request, opts media.Options) (media.Result, error) {
	// Reject speech fields before HTTP.
	for _, bad := range []string{"voice_id", "speed", "pitch", "emotion", "language_boost"} {
		if _, ok := req.Params[bad]; ok {
			return media.Result{ErrorCode: "invalid_request", ErrorMessage: "speech field not allowed on music_generation"}, nil
		}
	}
	base := strings.TrimRight(m.BaseURL, "/")
	if base == "" {
		base = "https://api.minimax.io"
	}
	doer := m.Doer
	if doer == nil {
		doer = defaultDoer()
	}
	authz, err := bearerAuth(opts)
	if err != nil {
		return media.Result{}, err
	}
	body := map[string]any{"model": req.Model, "prompt": req.Prompt}
	for k, v := range req.Params {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/music_generation", bytes.NewReader(raw))
	httpReq.Header.Set("Authorization", authz)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := doer.Do(httpReq)
	res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "minimax-music", AuthID: opts.PinnedAuthID}}
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	res.HTTPResponded = true
	b, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(b, &parsed)
	if br, ok := parsed["base_resp"].(map[string]any); ok {
		if code, ok := br["status_code"].(float64); ok && code != 0 {
			res.ErrorCode = fmt.Sprintf("base_resp_%d", int(code))
			res.ErrorMessage = "provider business error"
			res.Status = "failed"
			return res, nil
		}
	}
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		res.ErrorCode = "empty_response"
		res.Status = "failed"
		return res, nil
	}
	if st, ok := data["status"].(float64); ok {
		if int(st) == 1 {
			res.Status = "in_progress"
		} else if int(st) == 2 {
			res.Status = "completed"
		}
	}
	audio, _ := data["audio"].(string)
	if res.Status == "completed" {
		if audio == "" {
			res.ErrorCode = "empty_audio"
			res.Status = "failed"
			return res, nil
		}
		if strings.HasPrefix(audio, "http") {
			res.Assets = []media.Asset{{RemoteURL: audio, MimeType: "audio/mpeg"}}
		} else {
			decoded, err := hex.DecodeString(audio)
			if err != nil || len(decoded) == 0 {
				res.ErrorCode = "malformed_hex_audio"
				res.Status = "failed"
				return res, nil
			}
			res.Assets = []media.Asset{{Data: decoded, MimeType: "audio/mpeg"}}
			res.SyncComplete = true
		}
	}
	return res, nil
}

var _ media.Executor = (*MiniMaxMusic)(nil)

// --- Kling ---

type Kling struct {
	BaseURL string
	Doer    HTTPDoer
}

func (k *Kling) Identifier() string { return "kling" }
func (k *Kling) Operations() []media.Operation {
	return []media.Operation{media.OpImageToVideo, media.OpVideoGeneration}
}

func (k *Kling) ExecuteMedia(ctx context.Context, req media.Request, opts media.Options) (media.Result, error) {
	// Fail closed on legacy JWT shape for kling-3.0.
	if opts.Headers != nil {
		if opts.Headers.Get("X-Kling-Auth-Scheme") == "legacy_jwt" {
			return media.Result{ErrorCode: "invalid_auth", ErrorMessage: "kling-3.0 requires bearer API key, not legacy JWT"}, nil
		}
	}
	authz, err := bearerAuth(opts)
	if err != nil {
		return media.Result{}, err
	}
	base := strings.TrimRight(k.BaseURL, "/")
	if base == "" {
		base = "https://api-singapore.klingai.com"
	}
	doer := k.Doer
	if doer == nil {
		doer = defaultDoer()
	}
	switch req.Phase {
	case media.PhaseCreate:
		body := map[string]any{"model_name": req.Model, "prompt": req.Prompt}
		for k, v := range req.Params {
			body[k] = v
		}
		raw, _ := json.Marshal(body)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/image-to-video/kling-3.0", bytes.NewReader(raw))
		httpReq.Header.Set("Authorization", authz)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "kling", AuthID: opts.PinnedAuthID}}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		if data, ok := parsed["data"].(map[string]any); ok {
			if tid, ok := data["task_id"].(string); ok {
				res.Handle, _ = json.Marshal(map[string]string{"task_id": tid})
				res.AcceptedHandle = true
			}
			if st, ok := data["task_status"].(string); ok {
				res.Status = mapKlingStatus(st)
			}
		}
		return res, nil
	case media.PhaseStatus:
		var handle struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(req.Handle, &handle)
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/tasks?task_ids="+handle.TaskID, nil)
		httpReq.Header.Set("Authorization", authz)
		resp, err := doer.Do(httpReq)
		res := media.Result{SelectedAuth: media.SelectedAuth{Provider: "kling", AuthID: opts.PinnedAuthID}, Handle: req.Handle}
		if err != nil {
			return res, err
		}
		defer resp.Body.Close()
		res.HTTPResponded = true
		b, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		if data, ok := parsed["data"].(map[string]any); ok {
			if tasks, ok := data["tasks"].([]any); ok && len(tasks) > 0 {
				if task, ok := tasks[0].(map[string]any); ok {
					if st, ok := task["task_status"].(string); ok {
						res.Status = mapKlingStatus(st)
					}
					if outs, ok := task["outputs"].([]any); ok && len(outs) > 0 {
						if o0, ok := outs[0].(map[string]any); ok {
							if u, ok := o0["url"].(string); ok {
								res.Assets = []media.Asset{{RemoteURL: u, MimeType: "video/mp4"}}
							}
						}
					}
				}
			}
		}
		return res, nil
	default:
		return media.Result{}, fmt.Errorf("kling: unsupported phase %s", req.Phase)
	}
}

func mapKlingStatus(st string) string {
	switch st {
	case "submitted":
		return "queued"
	case "processing":
		return "in_progress"
	case "succeeded":
		return "completed"
	case "failed":
		return "failed"
	default:
		return "in_progress"
	}
}

var _ media.Executor = (*Kling)(nil)

func bearerAuth(opts media.Options) (string, error) {
	if opts.Headers != nil {
		if v := opts.Headers.Get("Authorization"); v != "" {
			return v, nil
		}
		if k := opts.Headers.Get("X-API-Key"); k != "" {
			return "Bearer " + k, nil
		}
	}
	return "", fmt.Errorf("missing bearer API key")
}

