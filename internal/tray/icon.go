package tray

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
)

const (
	iconSize                   = 32
	iconScale                  = 4
	iconCornerSize             = 4
	maxManagementAssetIconRead = 8 << 20
)

var (
	iconBackground         = color.RGBA{R: 249, G: 115, B: 22, A: 255}
	iconForeground         = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	faviconDataURLPrefixRE = regexp.MustCompile(`(?i)data:\s*image/(jpeg|jpg|png)\s*;\s*base64\s*,\s*`)
)

func trayIconPNG(managementAssetPath string) ([]byte, error) {
	if favicon, err := loadFaviconImage(managementAssetPath); err == nil {
		return encodeRoundedSquareIcon(favicon)
	}
	return generatedTrayIconPNG()
}

func loadFaviconImage(managementAssetPath string) (image.Image, error) {
	managementAssetPath = strings.TrimSpace(managementAssetPath)
	if managementAssetPath == "" {
		return nil, errors.New("empty management asset path")
	}

	file, err := os.Open(managementAssetPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxManagementAssetIconRead))
	if err != nil {
		return nil, err
	}
	return decodeFirstImageDataURL(string(data))
}

func decodeFirstImageDataURL(content string) (image.Image, error) {
	match := faviconDataURLPrefixRE.FindStringSubmatchIndex(content)
	if match == nil {
		return nil, errors.New("favicon data URL not found")
	}

	imageType := strings.ToLower(content[match[2]:match[3]])
	start := match[1]
	end := start
	for end < len(content) && isBase64Byte(content[end]) {
		end++
	}
	if end == start {
		return nil, errors.New("favicon data URL is empty")
	}

	raw, err := decodeBase64Image(content[start:end])
	if err != nil {
		return nil, err
	}

	reader := bytes.NewReader(raw)
	switch imageType {
	case "jpeg", "jpg":
		return jpeg.Decode(reader)
	case "png":
		return png.Decode(reader)
	default:
		return nil, fmt.Errorf("unsupported favicon image type %q", imageType)
	}
}

func isBase64Byte(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '+' ||
		b == '/' ||
		b == '=' ||
		b == '_' ||
		b == '-'
}

func decodeBase64Image(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}

	var lastErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("decode favicon base64: %w", lastErr)
}

func encodeRoundedSquareIcon(src image.Image) ([]byte, error) {
	if src == nil {
		return nil, errors.New("nil favicon image")
	}
	bounds := src.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, fmt.Errorf("invalid favicon bounds %v", bounds)
	}

	srcSize := iconSize * iconScale
	img := image.NewRGBA(image.Rect(0, 0, srcSize, srcSize))
	drawCenterCropped(img, src)
	applyRoundedCorners(img, iconCornerSize*iconScale)

	downsampled := downsampleRGBA(img, iconScale)
	return encodePNG(downsampled)
}

func drawCenterCropped(dst *image.RGBA, src image.Image) {
	srcBounds := src.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()
	srcSide := srcWidth
	if srcHeight < srcSide {
		srcSide = srcHeight
	}
	crop := image.Rect(
		srcBounds.Min.X+(srcWidth-srcSide)/2,
		srcBounds.Min.Y+(srcHeight-srcSide)/2,
		srcBounds.Min.X+(srcWidth+srcSide)/2,
		srcBounds.Min.Y+(srcHeight+srcSide)/2,
	)

	dstBounds := dst.Bounds()
	dstWidth := dstBounds.Dx()
	dstHeight := dstBounds.Dy()
	for y := dstBounds.Min.Y; y < dstBounds.Max.Y; y++ {
		srcY := crop.Min.Y + ((y-dstBounds.Min.Y)*crop.Dy())/dstHeight
		for x := dstBounds.Min.X; x < dstBounds.Max.X; x++ {
			srcX := crop.Min.X + ((x-dstBounds.Min.X)*crop.Dx())/dstWidth
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
}

func applyRoundedCorners(img *image.RGBA, radius int) {
	bounds := img.Bounds()
	size := bounds.Dx()
	radiusF := float64(radius)
	max := size - radius

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		localY := y - bounds.Min.Y
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			localX := x - bounds.Min.X
			if insideRoundedRect(localX, localY, size, radius, radiusF, max) {
				continue
			}

			offset := img.PixOffset(x, y)
			img.Pix[offset+3] = 0
		}
	}
}

func generatedTrayIconPNG() ([]byte, error) {
	srcSize := iconSize * iconScale
	img := image.NewRGBA(image.Rect(0, 0, srcSize, srcSize))

	drawRoundedRect(img, srcSize, iconCornerSize*iconScale, iconBackground)
	drawGlyphC(img, iconScale, iconForeground)

	downsampled := downsampleRGBA(img, iconScale)
	return encodePNG(downsampled)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawRoundedRect(img *image.RGBA, size int, radius int, c color.RGBA) {
	radiusF := float64(radius)
	max := size - radius

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if insideRoundedRect(x, y, size, radius, radiusF, max) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func insideRoundedRect(x int, y int, size int, radius int, radiusF float64, max int) bool {
	if x >= radius && x < max {
		return true
	}
	if y >= radius && y < max {
		return true
	}

	var cx int
	if x < radius {
		cx = radius
	} else {
		cx = size - radius - 1
	}

	var cy int
	if y < radius {
		cy = radius
	} else {
		cy = size - radius - 1
	}

	dx := float64(x - cx)
	dy := float64(y - cy)
	return math.Sqrt(dx*dx+dy*dy) <= radiusF
}

func drawGlyphC(img *image.RGBA, scale int, c color.RGBA) {
	fillRect(img, 10*scale, 7*scale, 24*scale, 11*scale, c)
	fillRect(img, 7*scale, 10*scale, 12*scale, 22*scale, c)
	fillRect(img, 10*scale, 21*scale, 24*scale, 25*scale, c)
	fillRect(img, 10*scale, 8*scale, 14*scale, 12*scale, c)
	fillRect(img, 10*scale, 20*scale, 14*scale, 24*scale, c)
}

func fillRect(img *image.RGBA, minX int, minY int, maxX int, maxY int, c color.RGBA) {
	bounds := img.Bounds()
	if minX < bounds.Min.X {
		minX = bounds.Min.X
	}
	if minY < bounds.Min.Y {
		minY = bounds.Min.Y
	}
	if maxX > bounds.Max.X {
		maxX = bounds.Max.X
	}
	if maxY > bounds.Max.Y {
		maxY = bounds.Max.Y
	}

	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func downsampleRGBA(src *image.RGBA, scale int) *image.RGBA {
	srcBounds := src.Bounds()
	dstWidth := srcBounds.Dx() / scale
	dstHeight := srcBounds.Dy() / scale
	dst := image.NewRGBA(image.Rect(0, 0, dstWidth, dstHeight))

	for y := 0; y < dstHeight; y++ {
		for x := 0; x < dstWidth; x++ {
			var r, g, b, a uint32
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					pr, pg, pb, pa := src.At(x*scale+sx, y*scale+sy).RGBA()
					r += pr
					g += pg
					b += pb
					a += pa
				}
			}
			pixels := uint32(scale * scale)
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((r / pixels) >> 8),
				G: uint8((g / pixels) >> 8),
				B: uint8((b / pixels) >> 8),
				A: uint8((a / pixels) >> 8),
			})
		}
	}

	return dst
}
