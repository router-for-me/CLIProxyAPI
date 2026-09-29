package jsplugin

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// MaxIconDataURIBytes bounds an uploaded plugin logo. It is deliberately
// generous: the plugin source itself is capped at 1 MiB, so this only keeps a
// logo from dwarfing the plugin it decorates.
const MaxIconDataURIBytes = 512 * 1024

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// DecodeIconDataURI parses a plugin logo shipped as a sidecar icon.svg or
// icon.png file and re-encoded by the uploader as
// data:image/png;base64,... or data:image/svg+xml;base64,.... It returns the
// media type and the raw image bytes. The admin UI renders logos only through
// <img>, which already blocks scripts and external loads; the SVG checks here
// are defense in depth so a hostile payload is rejected at upload instead of
// being stored.
func DecodeIconDataURI(icon string) (string, []byte, error) {
	if len(icon) > MaxIconDataURIBytes {
		return "", nil, fmt.Errorf("plugin icon must not exceed %d bytes", MaxIconDataURIBytes)
	}
	mediaType, payload, ok := strings.Cut(strings.TrimPrefix(icon, "data:"), ";base64,")
	if !strings.HasPrefix(icon, "data:") || !ok || (mediaType != "image/png" && mediaType != "image/svg+xml") {
		return "", nil, fmt.Errorf("plugin icon must be data:image/png;base64,... or data:image/svg+xml;base64,...")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("plugin icon payload is not valid base64")
	}
	if err := ValidateIconImage(mediaType, decoded); err != nil {
		return "", nil, err
	}
	return mediaType, decoded, nil
}

// ValidateIconImage checks raw PNG or SVG bytes for a plugin logo. PNG must
// carry the PNG signature. SVG must be well-formed XML rooted at svg without
// script or foreignObject elements, event-handler attributes, directives,
// javascript: values, or absolute http(s) references.
func ValidateIconImage(mediaType string, data []byte) error {
	if mediaType == "image/png" {
		if !bytes.HasPrefix(data, pngSignature) {
			return fmt.Errorf("plugin icon PNG payload is not a PNG image")
		}
		return nil
	}
	if mediaType != "image/svg+xml" {
		return fmt.Errorf("plugin icon must be image/png or image/svg+xml")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rootSeen := false
	styleDepth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("plugin icon SVG is not well-formed XML")
		}
		switch element := token.(type) {
		case xml.Directive:
			return fmt.Errorf("plugin icon SVG must not contain a DOCTYPE or other directives")
		case xml.ProcInst:
			if element.Target != "xml" {
				return fmt.Errorf("plugin icon SVG must not contain processing instructions")
			}
		case xml.StartElement:
			local := strings.ToLower(element.Name.Local)
			if !rootSeen {
				if local != "svg" {
					return fmt.Errorf("plugin icon SVG root element must be svg")
				}
				rootSeen = true
			}
			if local == "script" || local == "foreignobject" {
				return fmt.Errorf("plugin icon SVG must not contain %s elements", element.Name.Local)
			}
			if local == "style" {
				styleDepth++
			}
			for _, attr := range element.Attr {
				if attr.Name.Space == "xmlns" || (attr.Name.Space == "" && attr.Name.Local == "xmlns") {
					continue
				}
				if strings.HasPrefix(strings.ToLower(attr.Name.Local), "on") {
					return fmt.Errorf("plugin icon SVG must not contain event handler attributes")
				}
				if svgValueReferencesExternal(attr.Value) {
					return fmt.Errorf("plugin icon SVG must not reference scripts or external resources")
				}
			}
		case xml.EndElement:
			if strings.ToLower(element.Name.Local) == "style" && styleDepth > 0 {
				styleDepth--
			}
		case xml.CharData:
			if styleDepth > 0 && (svgValueReferencesExternal(string(element)) || strings.Contains(strings.ToLower(string(element)), "@import")) {
				return fmt.Errorf("plugin icon SVG must not reference scripts or external resources")
			}
		}
	}
	if !rootSeen {
		return fmt.Errorf("plugin icon SVG root element must be svg")
	}
	return nil
}

func svgValueReferencesExternal(value string) bool {
	compact := strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return -1
		}
		return unicode.ToLower(character)
	}, value)
	return strings.Contains(compact, "javascript:") || strings.Contains(compact, "http://") || strings.Contains(compact, "https://")
}
