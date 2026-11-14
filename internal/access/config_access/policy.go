package configaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (p *provider) authorizeRequest(policy sdkconfig.APIKeyPolicy, r *http.Request) *sdkaccess.AuthError {
	if len(policy.AllowedModels) == 0 && len(policy.BlockedModels) == 0 {
		return nil
	}
	model, err := p.requestModel(r)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return sdkaccess.NewRequestEntityTooLargeError(err)
		}
		log.WithField("client", policy.Name).Warn("client model policy rejected an unidentifiable model")
		return sdkaccess.NewDeniedError("Cannot authorize request model", http.StatusForbidden)
	}
	if model == "" {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if r.Method == http.MethodGet && (strings.HasSuffix(path, "/models") || strings.HasSuffix(path, "/responses")) {
			return nil
		}
		return sdkaccess.NewDeniedError("A model is required for this client key", http.StatusForbidden)
	}
	baseModel := strings.TrimSpace(model)
	if index := strings.LastIndex(baseModel, "("); index >= 0 && strings.HasSuffix(baseModel, ")") {
		baseModel = strings.TrimSpace(baseModel[:index])
	}
	if strings.EqualFold(baseModel, "auto") {
		return sdkaccess.NewDeniedError("An explicit model is required for this client key", http.StatusForbidden)
	}
	if !policy.Allowed(model) {
		log.WithFields(log.Fields{"client": policy.Name, "model": model}).Warn("client model policy denied request")
		return sdkaccess.NewDeniedError(fmt.Sprintf("Model %q is not allowed for this client key", model), http.StatusForbidden)
	}
	return nil
}

type replayPolicyBody struct {
	io.Reader
	source io.ReadCloser
}

func (b *replayPolicyBody) Close() error { return b.source.Close() }

type policyUploadBody struct {
	*os.File
	source io.ReadCloser
	once   sync.Once
}

func (b *policyUploadBody) Close() error {
	var err error
	b.once.Do(func() {
		err = b.File.Close()
		_ = os.Remove(b.File.Name())
		if closeErr := b.source.Close(); err == nil {
			err = closeErr
		}
	})
	return err
}

func (p *provider) requestModel(r *http.Request) (string, error) {
	if r == nil || r.URL == nil {
		return "", errors.New("missing request")
	}
	path := r.URL.Path
	if index := strings.Index(path, "/models/"); index >= 0 && (strings.HasPrefix(path, "/v1beta/") || r.Method == http.MethodGet) {
		model := path[index+len("/models/"):]
		if colon := strings.IndexByte(model, ':'); colon >= 0 {
			model = model[:colon]
		}
		return strings.TrimSpace(model), nil
	}
	if r.Method == http.MethodGet {
		model := strings.TrimSpace(r.URL.Query().Get("model"))
		if model == "" && strings.HasSuffix(path, "/realtime") {
			model = "gpt-realtime"
		}
		return model, nil
	}
	if r.Body == nil || r.Body == http.NoBody {
		return defaultRequestModel(path), nil
	}
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		return p.multipartModel(r, params["boundary"])
	}
	original := r.Body
	raw, err := io.ReadAll(original)
	r.Body = &replayPolicyBody{Reader: bytes.NewReader(raw), source: original}
	if err != nil {
		return "", err
	}
	if mediaType == "application/x-www-form-urlencoded" {
		form, errForm := url.ParseQuery(string(raw))
		if errForm != nil {
			return "", errForm
		}
		values := form["model"]
		if len(values) > 1 {
			return "", errors.New("ambiguous form model")
		}
		model := strings.TrimSpace(form.Get("model"))
		if model == "" {
			model = defaultRequestModel(path)
		}
		return model, nil
	}
	body := raw
	encodings := strings.Split(r.Header.Get("Content-Encoding"), ",")
	for index := len(encodings) - 1; index >= 0; index-- {
		switch strings.ToLower(strings.TrimSpace(encodings[index])) {
		case "", "identity":
		case "zstd":
			decoder, errDecode := zstd.NewReader(bytes.NewReader(body))
			if errDecode != nil {
				if json.Valid(raw) {
					body = raw
					continue
				}
				return "", errDecode
			}
			var reader io.Reader = decoder
			if p.maxRequestBodyBytes > 0 {
				reader = io.LimitReader(reader, p.maxRequestBodyBytes+1)
			}
			decoded, errRead := io.ReadAll(reader)
			decoder.Close()
			if errRead != nil {
				return "", errRead
			}
			if p.maxRequestBodyBytes > 0 && int64(len(decoded)) > p.maxRequestBodyBytes {
				return "", &http.MaxBytesError{Limit: p.maxRequestBodyBytes}
			}
			body = decoded
		default:
			if json.Valid(raw) {
				body = raw
				continue
			}
			return "", errors.New("unsupported encoding")
		}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return defaultRequestModel(path), nil
	}
	model, err := jsonRequestModel(body, requestJSONSelector(path))
	if err != nil {
		return "", err
	}
	if model == "" {
		model = defaultRequestModel(path)
	}
	if strings.HasSuffix(path, "/interactions") {
		model = strings.TrimPrefix(model, "models/")
	}
	return model, nil
}

func requestJSONSelector(path string) string {
	if strings.HasSuffix(path, "/realtime/client_secrets") {
		return "session"
	}
	if strings.HasSuffix(path, "/realtime/sessions") {
		return "model"
	}
	if strings.Contains(path, "/realtime") || strings.HasSuffix(path, "/live") {
		return "nested"
	}
	if strings.HasSuffix(path, "/interactions") {
		return "interaction"
	}
	return "model"
}

func jsonRequestModel(body []byte, selector string) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("invalid model request")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return "", err
		}
		key, ok := token.(string)
		if !ok {
			return "", errors.New("invalid field")
		}
		if lower := strings.ToLower(key); lower != key && (lower == "model" || lower == "session" || lower == "agent") {
			return "", errors.New("ambiguous model field case")
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return "", err
		}
		if _, exists := fields[key]; exists && (key == "model" || key == "session" || key == "agent") {
			return "", errors.New("ambiguous model")
		}
		fields[key] = value
	}
	if _, err = decoder.Token(); err != nil {
		return "", err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return "", errors.New("trailing JSON")
	}
	if (selector == "nested" || selector == "session") && fields["session"] != nil && string(fields["session"]) != "null" {
		model, errSession := jsonRequestModel(fields["session"], "model")
		if errSession != nil {
			return "", errSession
		}
		if model != "" {
			return model, nil
		}
	}
	if selector == "session" {
		return "", nil
	}
	var model string
	if fields["model"] != nil {
		if err = json.Unmarshal(fields["model"], &model); err != nil {
			return "", err
		}
	}
	if selector == "interaction" && model == "" && fields["agent"] != nil {
		if err = json.Unmarshal(fields["agent"], &model); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(model), nil
}

func defaultRequestModel(path string) string {
	switch {
	case strings.Contains(path, "/images/"):
		return "gpt-image-2"
	case strings.HasSuffix(path, "/audio/speech") || strings.HasSuffix(path, "/tts"):
		return "grok-tts"
	case strings.HasSuffix(path, "/openai/v1/videos"):
		return "sora-2"
	case strings.HasSuffix(path, "/videos"), strings.HasSuffix(path, "/videos/generations"), strings.HasSuffix(path, "/videos/edits"), strings.HasSuffix(path, "/videos/extensions"):
		return "grok-imagine-video"
	case strings.Contains(path, "/realtime/client_secrets"), strings.HasSuffix(path, "/realtime/sessions"):
		return "gpt-realtime"
	case strings.HasSuffix(path, "/live"), strings.HasSuffix(path, "/realtime/calls"), strings.HasSuffix(path, "/realtime"):
		return "gpt-live-1-codex"
	}
	return ""
}

func (p *provider) multipartModel(r *http.Request, boundary string) (string, error) {
	file, err := os.CreateTemp("", "cpa-model-policy-*")
	if err != nil {
		return "", err
	}
	replay := &policyUploadBody{File: file, source: r.Body}
	context.AfterFunc(r.Context(), func() { _ = replay.Close() })
	r.Body = replay
	if _, err = io.Copy(file, replay.source); err != nil {
		_ = replay.Close()
		return "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		_ = replay.Close()
		return "", err
	}
	defer func() { _, _ = file.Seek(0, io.SeekStart) }()
	reader := multipart.NewReader(file, boundary)
	model := ""
	sessionModel := ""
	seen := make(map[string]bool)
	for {
		part, errPart := reader.NextPart()
		if errPart == io.EOF {
			break
		}
		if errPart != nil {
			return "", errPart
		}
		name := part.FormName()
		liveRequest := strings.Contains(r.URL.Path, "/realtime") || strings.HasSuffix(r.URL.Path, "/live")
		if (name == "model" && part.FileName() == "") || (name == "session" && liveRequest) {
			if seen[name] {
				_ = part.Close()
				return "", errors.New("ambiguous model field")
			}
			seen[name] = true
			value, errRead := io.ReadAll(io.LimitReader(part, 1<<20+1))
			_ = part.Close()
			if errRead != nil {
				return "", errRead
			}
			if len(value) > 1<<20 {
				return "", errors.New("model field too large")
			}
			if name == "model" {
				model = strings.TrimSpace(string(value))
			} else {
				sessionModel, err = jsonRequestModel(value, "nested")
				if err != nil {
					return "", err
				}
			}
		} else {
			if err = part.Close(); err != nil {
				return "", err
			}
		}
	}
	if strings.Contains(r.URL.Path, "/realtime") || strings.HasSuffix(r.URL.Path, "/live") {
		model = sessionModel
	}
	if model == "" {
		model = defaultRequestModel(r.URL.Path)
	}
	return model, nil
}
