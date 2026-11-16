package logging

import (
	"errors"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

func encodeCaptureEvent(e CaptureEvent) ([]byte, error) {
	doc := ojson.NewDocString("{}")
	if err := doc.Set("record", e); err != nil {
		return nil, err
	}
	return doc.Bytes(), nil
}
func decodeCaptureEvent(data []byte, e *CaptureEvent) error {
	if !ojson.ValidBytes(data) {
		return errors.New("invalid capture event")
	}
	r := ojson.ParseBytes(data).Get("record")
	if !r.IsObject() {
		return errors.New("invalid capture record")
	}
	*e = CaptureEvent{CaptureRecord: CaptureRecord{
		ID: r.Get("id").String(), TraceID: r.Get("traceId").String(), Epoch: r.Get("epoch").String(), Sequence: r.Get("sequence").Uint(),
		StartedAt: r.Get("startedAt").String(), Method: r.Get("method").String(), Path: r.Get("path").String(), Status: int(r.Get("status").Int()),
		State: r.Get("state").String(), Reason: r.Get("reason").String(), Request: r.Get("request").String(), Response: r.Get("response").String(),
		RequestType: r.Get("requestType").String(), ResponseType: r.Get("responseType").String(),
		RequestEncoding: r.Get("requestEncoding").String(), ResponseEncoding: r.Get("responseEncoding").String(), Format: r.Get("format").String(),
		RequestDecoded: r.Get("requestDecoded").Bool(), ResponseDecoded: r.Get("responseDecoded").Bool(),
	}, Event: r.Get("event").String(), RequestBytes: r.Get("requestBytes").Uint(), ResponseBytes: r.Get("responseBytes").Uint(),
		FrameCount: r.Get("frameCount").Uint(), RequestSHA256: r.Get("requestSha256").String(), ResponseSHA256: r.Get("responseSha256").String()}
	if c := r.Get("chunk"); c.IsObject() {
		payload, err := base64.StdEncoding.DecodeString(c.Get("data").String())
		if err != nil {
			return err
		}
		e.Chunk = &CaptureChunk{Direction: c.Get("direction").String(), Offset: c.Get("offset").Uint(), Data: payload,
			Frame: c.Get("frame").Uint(), Type: c.Get("type").String(), At: c.Get("at").String(), End: c.Get("end").Bool()}
	}
	return nil
}
