package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
)

type applyPatchJSONPhase uint8

const (
	patchBeforeObject applyPatchJSONPhase = iota
	patchBeforeKey
	patchInKey
	patchBeforeColon
	patchBeforeValue
	patchInValue
	patchAfterValue
	patchComplete
)

// ApplyPatchInputDecoder decodes the input string from streamed function arguments.
// A decoder belongs to one call and must not be copied after use.
type ApplyPatchInputDecoder struct {
	phase         applyPatchJSONPhase
	keyRaw        []byte
	escapeRaw     []byte
	utf8Pending   []byte
	highSurrogate uint16
	input         strings.Builder
	finished      bool
	err           error
}

// Push scans each fragment once, emitting only complete, validated characters.
func (d *ApplyPatchInputDecoder) Push(fragment string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	if d.finished {
		if fragment == "" {
			return "", nil
		}
		return "", d.fail(errors.New("apply_patch arguments received after completion"))
	}
	start := d.input.Len()
	for i := 0; i < len(fragment); i++ {
		c := fragment[i]
		switch d.phase {
		case patchBeforeObject:
			if applyPatchJSONSpace(c) {
				continue
			}
			if c != '{' {
				return "", d.fail(errors.New("apply_patch arguments must be a JSON object"))
			}
			d.phase = patchBeforeKey
		case patchBeforeKey:
			if applyPatchJSONSpace(c) {
				continue
			}
			if c != '"' {
				return "", d.fail(errors.New("apply_patch arguments must contain the input field"))
			}
			d.keyRaw = append(d.keyRaw, c)
			d.phase = patchInKey
		case patchInKey:
			d.keyRaw = append(d.keyRaw, c)
			if len(d.escapeRaw) != 0 {
				d.escapeRaw = d.escapeRaw[:0]
				continue
			}
			if c == '\\' {
				d.escapeRaw = append(d.escapeRaw, c)
				continue
			}
			if c < 0x20 {
				return "", d.fail(errors.New("invalid control character in apply_patch input key"))
			}
			if c == '"' {
				var key string
				if errUnmarshal := json.Unmarshal(d.keyRaw, &key); errUnmarshal != nil {
					return "", d.fail(fmt.Errorf("decode apply_patch input key: %w", errUnmarshal))
				}
				if key != "input" {
					return "", d.fail(errors.New("apply_patch arguments must contain the input field"))
				}
				d.keyRaw = nil
				d.phase = patchBeforeColon
			}
		case patchBeforeColon:
			if applyPatchJSONSpace(c) {
				continue
			}
			if c != ':' {
				return "", d.fail(errors.New("apply_patch input key must be followed by a colon"))
			}
			d.phase = patchBeforeValue
		case patchBeforeValue:
			if applyPatchJSONSpace(c) {
				continue
			}
			if c != '"' {
				return "", d.fail(errors.New("apply_patch input must be a string"))
			}
			d.phase = patchInValue
		case patchInValue:
			if errConsumeValue := d.consumeValue(c); errConsumeValue != nil {
				return "", d.fail(errConsumeValue)
			}
		case patchAfterValue:
			if applyPatchJSONSpace(c) {
				continue
			}
			if c != '}' {
				return "", d.fail(errors.New("apply_patch arguments must contain only one input field"))
			}
			d.phase = patchComplete
		case patchComplete:
			if !applyPatchJSONSpace(c) {
				return "", d.fail(errors.New("apply_patch arguments must not contain trailing JSON"))
			}
		}
	}
	return d.input.String()[start:], nil
}

func (d *ApplyPatchInputDecoder) consumeValue(c byte) error {
	if len(d.utf8Pending) != 0 || c >= utf8.RuneSelf {
		// Pending escapes and surrogate pairs cannot consume raw UTF-8.
		if len(d.escapeRaw) != 0 || d.highSurrogate != 0 {
			return errors.New("invalid Unicode escape in apply_patch input")
		}
		d.utf8Pending = append(d.utf8Pending, c)
		if !utf8.FullRune(d.utf8Pending) {
			return nil
		}
		r, size := utf8.DecodeRune(d.utf8Pending)
		if (r == utf8.RuneError && size == 1) || size != len(d.utf8Pending) {
			return errors.New("invalid UTF-8 in apply_patch input")
		}
		d.input.Write(d.utf8Pending)
		d.utf8Pending = d.utf8Pending[:0]
		return nil
	}
	if len(d.escapeRaw) != 0 {
		d.escapeRaw = append(d.escapeRaw, c)
		if len(d.escapeRaw) == 2 {
			if d.highSurrogate != 0 && c != 'u' {
				return errors.New("apply_patch input high surrogate requires a low surrogate")
			}
			var decoded byte
			switch c {
			case 'u':
				return nil
			case '"', '\\', '/':
				decoded = c
			case 'b':
				decoded = '\b'
			case 'f':
				decoded = '\f'
			case 'n':
				decoded = '\n'
			case 'r':
				decoded = '\r'
			case 't':
				decoded = '\t'
			default:
				return errors.New("invalid escape in apply_patch input")
			}
			d.input.WriteByte(decoded)
			d.escapeRaw = d.escapeRaw[:0]
			return nil
		}
		if _, ok := applyPatchHex(c); !ok {
			return errors.New("invalid Unicode escape in apply_patch input")
		}
		if len(d.escapeRaw) < 6 {
			return nil
		}
		var code uint16
		for _, digit := range d.escapeRaw[2:] {
			nibble, _ := applyPatchHex(digit)
			code = code<<4 | nibble
		}
		d.escapeRaw = d.escapeRaw[:0]
		switch {
		case d.highSurrogate != 0:
			if code < 0xdc00 || code > 0xdfff {
				return errors.New("apply_patch input high surrogate requires a low surrogate")
			}
			d.input.WriteRune(utf16.DecodeRune(rune(d.highSurrogate), rune(code)))
			d.highSurrogate = 0
		case code >= 0xd800 && code <= 0xdbff:
			d.highSurrogate = code
		case code >= 0xdc00 && code <= 0xdfff:
			return errors.New("unpaired low surrogate in apply_patch input")
		default:
			d.input.WriteRune(rune(code))
		}
		return nil
	}
	if d.highSurrogate != 0 && c != '\\' {
		return errors.New("apply_patch input high surrogate requires a low surrogate")
	}
	switch {
	case c == '\\':
		d.escapeRaw = append(d.escapeRaw, c)
	case c == '"':
		d.phase = patchAfterValue
	case c < 0x20:
		return errors.New("invalid control character in apply_patch input")
	default:
		d.input.WriteByte(c)
	}
	return nil
}

// Finish validates the final wrapper and emits only the previously unsent suffix.
func (d *ApplyPatchInputDecoder) Finish(arguments string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	input, errUnwrapInput := applypatch.UnwrapInput(arguments)
	if errUnwrapInput != nil {
		return "", d.fail(errUnwrapInput)
	}
	// encoding/json replaces invalid UTF-8 and unpaired surrogates. The final
	// snapshot needs the same strict character validation as streamed fragments.
	var final ApplyPatchInputDecoder
	if _, errPush := final.Push(arguments); errPush != nil {
		return "", d.fail(errPush)
	}
	if d.finished {
		if input != d.Input() {
			return "", d.fail(errors.New("conflicting apply_patch arguments completion"))
		}
		return "", nil
	}
	if !strings.HasPrefix(input, d.Input()) {
		return "", d.fail(errors.New("final apply_patch input conflicts with streamed input"))
	}
	tail := input[d.input.Len():]
	d.input.WriteString(tail)
	d.finished = true
	d.phase = patchComplete
	d.keyRaw = nil
	d.escapeRaw = nil
	d.utf8Pending = nil
	d.highSurrogate = 0
	return tail, nil
}

// Input returns the decoded input, preserving its original whitespace.
func (d *ApplyPatchInputDecoder) Input() string {
	return d.input.String()
}

func (d *ApplyPatchInputDecoder) fail(err error) error {
	d.err = err
	return err
}

func applyPatchJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func applyPatchHex(c byte) (uint16, bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint16(c - '0'), true
	case c >= 'a' && c <= 'f':
		return uint16(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return uint16(c-'A') + 10, true
	default:
		return 0, false
	}
}
