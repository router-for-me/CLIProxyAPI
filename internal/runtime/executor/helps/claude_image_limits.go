package helps

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"net/http"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Anthropic vision request limits:
// https://platform.claude.com/docs/en/build-with-claude/vision#request-limits
//
// The docs give 2000 px per side once a request has more than 20 images. The
// API enforces 3000 px (measured 2026-10-20 on claude-opus-5-5 and
// claude-haiku-4-5: 21 images at 3000 px accepted, 21 at 3001 px rejected with
// "exceed max allowed size for many-image requests: 3000 pixels"), so the
// enforced value is used and images between 2000 and 3000 px stay untouched.
const (
	claudeRequestBodyLimit      = 32 << 20 // 33 554 432 bytes of the final JSON body
	claudeImageBase64Limit      = 10 << 20 // 10 485 760 bytes of base64 per image
	claudeImageMaxDimension     = 8000
	claudeManyImagesThreshold   = 20 // more than this many image blocks
	claudeManyImagesMaxLongSide = 3000
	claudeOversizeBodyLongSide  = 1024
	// claudeImageMaxDecodePixels bounds the raster decoded for re-encoding
	// (about 400 MB as RGBA). A larger image is left as it is and the final
	// validation rejects it locally instead of risking the whole process.
	claudeImageMaxDecodePixels = 100_000_000
	claudeImageJPEGQuality     = 85
)

const claudeOmittedImagePlaceholder = "[image omitted by the proxy: the request would exceed Anthropic's 32 MB limit; open the file again if it is still needed]"

// ClaudeImageLimitError is a local 400 for a request that Anthropic would
// reject for its size or image dimensions. It is request scoped, so no other
// credential is tried.
type ClaudeImageLimitError struct {
	msg string
}

func (e ClaudeImageLimitError) Error() string       { return e.msg }
func (ClaudeImageLimitError) StatusCode() int       { return http.StatusBadRequest }
func (ClaudeImageLimitError) IsRequestScoped() bool { return true }

func newClaudeImageLimitError(format string, args ...any) error {
	return ClaudeImageLimitError{msg: "invalid_request_error: request_too_large: " + fmt.Sprintf(format, args...)}
}

// claudeImageRef is one image block in request order (messages, then content,
// then tool_result content).
type claudeImageRef struct {
	block    gjson.Result
	position string
	format   string // "png", "jpeg" or "" when not a decodable base64 PNG/JPEG
	width    int
	height   int
	dataLen  int
	newRaw   []byte // replacement block, nil when unchanged
}

func collectClaudeImageBlocks(body []byte) []*claudeImageRef {
	var refs []*claudeImageRef
	add := func(block gjson.Result, position string) {
		ref := &claudeImageRef{block: block, position: position}
		source := block.Get("source")
		if source.Get("type").String() == "base64" {
			data := source.Get("data").String()
			ref.dataLen = len(data)
			cfg, format, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
			if err == nil && (format == "png" || format == "jpeg") {
				ref.format, ref.width, ref.height = format, cfg.Width, cfg.Height
			}
		}
		refs = append(refs, ref)
	}
	for i, message := range gjson.GetBytes(body, "messages").Array() {
		for j, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "image":
				add(block, fmt.Sprintf("messages.%d.content.%d", i, j))
			case "tool_result":
				for k, inner := range block.Get("content").Array() {
					if inner.Get("type").String() == "image" {
						add(inner, fmt.Sprintf("messages.%d.content.%d.content.%d", i, j, k))
					}
				}
			}
		}
	}
	return refs
}

// claudeImageRequestSize is the body size the image decisions use: the whole
// body without its top-level stream field, the only field in which the
// Messages and count_tokens paths can differ here. Both paths therefore reach
// the same image blocks for the same input, and a body that fits the limit is
// never reduced. A body over the limit by no more than the stream field is
// left for the final validation, which rejects it locally with its size.
func claudeImageRequestSize(body []byte) int {
	size := len(body)
	if value := gjson.GetBytes(body, "stream"); value.Exists() {
		size -= len(`,"stream":`) + len(value.Raw)
	}
	return size
}

// LimitClaudeRequestImages keeps base64 PNG/JPEG images inside Anthropic's
// request limits. The size decision normalizes only stream, so Execute,
// ExecuteStream and CountTokens reach the same image blocks for the same input.
// GIF, WebP and URL images are never re-encoded.
func LimitClaudeRequestImages(body []byte) []byte {
	refs := collectClaudeImageBlocks(body)
	if len(refs) == 0 {
		return body
	}
	manyImages := len(refs) > claudeManyImagesThreshold
	for _, ref := range refs {
		if ref.format == "" {
			continue
		}
		maxLongSide := 0
		if manyImages && max(ref.width, ref.height) > claudeManyImagesMaxLongSide {
			maxLongSide = claudeManyImagesMaxLongSide
		}
		overDimension := ref.width > claudeImageMaxDimension || ref.height > claudeImageMaxDimension
		if maxLongSide == 0 && !overDimension && ref.dataLen <= claudeImageBase64Limit {
			continue
		}
		if maxLongSide == 0 {
			maxLongSide = claudeImageMaxDimension
		}
		if raw, ok := reencodeClaudeImageBlock(ref, maxLongSide, false); ok {
			ref.newRaw = raw
		}
	}

	// Reduce only when the request is over the limit. Fields the executor adds
	// later (cloaking, metadata) can still push a request that fits here over
	// it; the final validation then rejects it locally with its size.
	budget := claudeRequestBodyLimit
	size := claudeImageRequestSize(body)
	for _, ref := range refs {
		if ref.newRaw != nil {
			size += len(ref.newRaw) - len(ref.block.Raw)
		}
	}
	if size > budget {
		// Step 1: shrink PNG/JPEG images to JPEG with a 1024 px long side,
		// oldest first, until the request fits.
		for _, ref := range refs {
			if size <= budget {
				break
			}
			if ref.format == "" {
				continue
			}
			raw, ok := reencodeClaudeImageBlock(ref, claudeOversizeBodyLongSide, true)
			if !ok {
				continue
			}
			current := len(ref.block.Raw)
			if ref.newRaw != nil {
				current = len(ref.newRaw)
			}
			if len(raw) < current {
				size += len(raw) - current
				ref.newRaw = raw
			}
		}
		// Step 2: replace images with a text block, oldest first, never the
		// newest one.
		var omitted []string
		for _, ref := range refs[:len(refs)-1] {
			if size <= budget {
				break
			}
			raw := claudeOmittedImageBlock(ref.block)
			current := len(ref.block.Raw)
			if ref.newRaw != nil {
				current = len(ref.newRaw)
			}
			size += len(raw) - current
			ref.newRaw = raw
			omitted = append(omitted, ref.position)
		}
		if len(omitted) > 0 {
			log.Warnf("claude executor: omitted %d image(s) to stay under Anthropic's 32 MB request limit: %s", len(omitted), strings.Join(omitted, ", "))
		}
	}
	return spliceClaudeImageBlocks(body, refs)
}

func spliceClaudeImageBlocks(body []byte, refs []*claudeImageRef) []byte {
	changed := make([]*claudeImageRef, 0, len(refs))
	for _, ref := range refs {
		if ref.newRaw != nil {
			changed = append(changed, ref)
		}
	}
	if len(changed) == 0 {
		return body
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].block.Index < changed[j].block.Index })
	var out bytes.Buffer
	last := 0
	for _, ref := range changed {
		start := ref.block.Index
		end := start + len(ref.block.Raw)
		if start < last || end > len(body) || string(body[start:end]) != ref.block.Raw {
			// Offsets do not match the body; leave it to the final validation.
			return body
		}
		out.Write(body[last:start])
		out.Write(ref.newRaw)
		last = end
	}
	out.Write(body[last:])
	return out.Bytes()
}

func claudeOmittedImageBlock(block gjson.Result) []byte {
	out := map[string]any{"type": "text", "text": claudeOmittedImagePlaceholder}
	if cc := block.Get("cache_control"); cc.Exists() {
		out["cache_control"] = json.RawMessage(cc.Raw)
	}
	raw, _ := json.Marshal(out)
	return raw
}

// reencodeClaudeImageBlock scales the image so its long side is at most
// maxLongSide (never upscaling) and its base64 fits the per-image limit. PNG
// stays PNG unless forceJPEG is set or the PNG would still be too large; JPEG
// output uses a white background for transparent pixels.
func reencodeClaudeImageBlock(ref *claudeImageRef, maxLongSide int, forceJPEG bool) ([]byte, bool) {
	if int64(ref.width)*int64(ref.height) > claudeImageMaxDecodePixels {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(ref.block.Get("source.data").String())
	if err != nil {
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	longSide := max(ref.width, ref.height)
	target := min(longSide, maxLongSide)
	useJPEG := forceJPEG || ref.format == "jpeg"
	for attempt := 0; attempt < 8 && target > 0; attempt++ {
		scaled := scaleClaudeImage(img, target)
		var buf bytes.Buffer
		mediaType := "image/png"
		if useJPEG {
			mediaType = "image/jpeg"
			err = jpeg.Encode(&buf, claudeImageOnWhite(scaled), &jpeg.Options{Quality: claudeImageJPEGQuality})
		} else {
			err = png.Encode(&buf, scaled)
		}
		if err != nil {
			return nil, false
		}
		if base64.StdEncoding.EncodedLen(buf.Len()) <= claudeImageBase64Limit {
			return claudeImageBlockWithSource(ref.block, mediaType, base64.StdEncoding.EncodeToString(buf.Bytes())), true
		}
		if !useJPEG {
			useJPEG = true
			continue
		}
		target = target * 3 / 4
	}
	return nil, false
}

func claudeImageBlockWithSource(block gjson.Result, mediaType, data string) []byte {
	source, _ := json.Marshal(map[string]string{"type": "base64", "media_type": mediaType, "data": data})
	// Keep every other field (cache_control, citations) and its order.
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	wroteSource := false
	block.ForEach(func(key, value gjson.Result) bool {
		if !first {
			out.WriteByte(',')
		}
		first = false
		out.WriteString(key.Raw)
		out.WriteByte(':')
		if key.String() == "source" {
			out.Write(source)
			wroteSource = true
		} else {
			out.WriteString(value.Raw)
		}
		return true
	})
	if !wroteSource {
		if !first {
			out.WriteByte(',')
		}
		out.WriteString(`"source":`)
		out.Write(source)
	}
	out.WriteByte('}')
	return out.Bytes()
}

// scaleClaudeImage downsizes with an area average so the long side equals
// target, returning src unchanged when it already fits. Source rows are
// converted one output row band at a time to bound memory on large images.
func scaleClaudeImage(src image.Image, target int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	long := max(sw, sh)
	if long <= target {
		return src
	}
	dw := max(1, (sw*target+long/2)/long)
	dh := max(1, (sh*target+long/2)/long)
	out := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	var band *image.NRGBA
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		rows := y1 - y0
		if band == nil || band.Rect.Dy() < rows {
			band = image.NewNRGBA(image.Rect(0, 0, sw, rows))
		}
		draw.Draw(band, image.Rect(0, 0, sw, rows), src, image.Pt(b.Min.X, b.Min.Y+y0), draw.Src)
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, a, n uint64
			for yy := 0; yy < rows; yy++ {
				row := band.Pix[yy*band.Stride:]
				for xx := x0; xx < x1; xx++ {
					p := row[xx*4 : xx*4+4]
					// Premultiply so transparent pixels do not bleed color.
					pa := uint64(p[3])
					r += uint64(p[0]) * pa
					g += uint64(p[1]) * pa
					bl += uint64(p[2]) * pa
					a += pa
					n++
				}
			}
			o := out.Pix[y*out.Stride+x*4:]
			if a > 0 {
				o[0], o[1], o[2] = uint8(r/a), uint8(g/a), uint8(bl/a)
			}
			o[3] = uint8(a / n)
		}
	}
	return out
}

// claudeImageOnWhite composites transparent pixels over white for JPEG output
// without copying the image. Opaque sources are returned as they are.
func claudeImageOnWhite(src image.Image) image.Image {
	if o, ok := src.(interface{ Opaque() bool }); ok && o.Opaque() {
		return src
	}
	return onWhiteImage{src}
}

type onWhiteImage struct{ image.Image }

func (onWhiteImage) ColorModel() color.Model { return color.RGBAModel }

func (m onWhiteImage) At(x, y int) color.Color {
	r, g, b, a := m.Image.At(x, y).RGBA()
	const full = 0xffff
	return color.RGBA64{
		R: uint16(r + (full - a)),
		G: uint16(g + (full - a)),
		B: uint16(b + (full - a)),
		A: full,
	}
}

// ValidateClaudeImageLimits checks the final body against Anthropic's request
// limits after payload rules ran, so a request that would be rejected fails
// locally without an upstream attempt.
func ValidateClaudeImageLimits(body []byte) error {
	if len(body) > claudeRequestBodyLimit {
		return newClaudeImageLimitError("request body is %d bytes, Anthropic accepts at most %d bytes", len(body), claudeRequestBodyLimit)
	}
	refs := collectClaudeImageBlocks(body)
	manyImages := len(refs) > claudeManyImagesThreshold
	for _, ref := range refs {
		if ref.dataLen > claudeImageBase64Limit && (ref.format != "" || ref.block.Get("source.media_type").String() == "image/png" || ref.block.Get("source.media_type").String() == "image/jpeg") {
			return newClaudeImageLimitError("image at %s is %d bytes of base64, Anthropic accepts at most %d bytes", ref.position, ref.dataLen, claudeImageBase64Limit)
		}
		if ref.format == "" {
			continue
		}
		if ref.width > claudeImageMaxDimension || ref.height > claudeImageMaxDimension {
			return newClaudeImageLimitError("image at %s is %dx%d px, Anthropic accepts at most %d px per side", ref.position, ref.width, ref.height, claudeImageMaxDimension)
		}
		if manyImages && max(ref.width, ref.height) > claudeManyImagesMaxLongSide {
			return newClaudeImageLimitError("image at %s is %dx%d px in a request with %d images, Anthropic accepts at most %d px per side when a request has more than %d images", ref.position, ref.width, ref.height, len(refs), claudeManyImagesMaxLongSide, claudeManyImagesThreshold)
		}
	}
	return nil
}
