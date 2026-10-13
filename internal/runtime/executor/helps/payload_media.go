package helps

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// ApplyMediaPayloadConfig exposes multipart fields as JSON business values. File
// parts use {filename, content_type, data} with base64 data; framing stays opaque.
func ApplyMediaPayloadConfig(cfg *config.Config, executor, model, protocol string, body []byte, contentType string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, string, error) {
	view, multipartBody, errDecode := mediaPayloadJSON(body, contentType)
	if errDecode != nil {
		return nil, "", errDecode
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalView, _, errOriginal := mediaPayloadJSON(original, opts.Headers.Get("Content-Type"))
	if errOriginal != nil {
		return nil, "", errOriginal
	}
	out := NewPayloadFinalizer(cfg, executor, model, protocol, "", originalView, req, opts)(view)
	if !multipartBody || bytes.Equal(out, view) {
		return outOrBody(out, body, multipartBody), contentType, nil
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for key, value := range gjson.ParseBytes(out).Map() {
		values := []gjson.Result{value}
		if value.IsArray() {
			values = value.Array()
		}
		for _, item := range values {
			if item.IsObject() && item.Get("filename").Exists() {
				data, errData := base64.StdEncoding.DecodeString(item.Get("data").String())
				if errData != nil {
					return nil, "", fmt.Errorf("multipart payload %s: %w", key, errData)
				}
				header := make(textproto.MIMEHeader)
				header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": key, "filename": item.Get("filename").String()}))
				header.Set("Content-Type", item.Get("content_type").String())
				part, errPart := writer.CreatePart(header)
				if errPart != nil {
					return nil, "", errPart
				}
				if _, errWrite := part.Write(data); errWrite != nil {
					return nil, "", errWrite
				}
			} else if errWrite := writer.WriteField(key, item.String()); errWrite != nil {
				return nil, "", errWrite
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", errClose
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func outOrBody(out, body []byte, multipartBody bool) []byte {
	if multipartBody {
		return body
	}
	return out
}

func mediaPayloadJSON(body []byte, contentType string) ([]byte, bool, error) {
	if json.Valid(body) {
		return body, false, nil
	}
	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || mediaType != "multipart/form-data" {
		return body, false, nil
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	fields := make(map[string]any)
	for {
		part, errNext := reader.NextPart()
		if errNext == io.EOF {
			break
		}
		if errNext != nil {
			return nil, false, errNext
		}
		data, errRead := io.ReadAll(part)
		if errClose := part.Close(); errClose != nil {
			return nil, false, errClose
		}
		if errRead != nil {
			return nil, false, errRead
		}
		var value any = string(data)
		if part.FileName() != "" {
			value = map[string]string{"filename": part.FileName(), "content_type": part.Header.Get("Content-Type"), "data": base64.StdEncoding.EncodeToString(data)}
		}
		key := part.FormName()
		if previous, ok := fields[key]; ok {
			values, isArray := previous.([]any)
			if !isArray {
				values = []any{previous}
			}
			fields[key] = append(values, value)
		} else {
			fields[key] = value
		}
	}
	view, errMarshal := json.Marshal(fields)
	return view, true, errMarshal
}
