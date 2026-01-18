package common

import (
	"unicode/utf8"
)

type UTF8StreamParser struct {
	buffer []byte
}

func NewUTF8StreamParser() *UTF8StreamParser {
	return &UTF8StreamParser{
		buffer: make([]byte, 0, 64),
	}
}

func (p *UTF8StreamParser) Write(data []byte) {
	p.buffer = append(p.buffer, data...)
}

func (p *UTF8StreamParser) Read() (string, bool) {
	if len(p.buffer) == 0 {
		return "", false
	}

	validLen := p.findValidUTF8End(p.buffer)
	if validLen == 0 {
		return "", false
	}

	result := string(p.buffer[:validLen])
	p.buffer = p.buffer[validLen:]

	return result, true
}

func (p *UTF8StreamParser) Flush() string {
	if len(p.buffer) == 0 {
		return ""
	}
	result := string(p.buffer)
	p.buffer = p.buffer[:0]
	return result
}

func (p *UTF8StreamParser) Reset() {
	p.buffer = p.buffer[:0]
}

func (p *UTF8StreamParser) findValidUTF8End(data []byte) int {
	if len(data) == 0 {
		return 0
	}

	end := len(data)
	for i := 1; i <= 3 && i <= len(data); i++ {
		b := data[len(data)-i]
		if b&0x80 == 0 {
			break
		}
		if b&0xC0 == 0xC0 {
			size := p.utf8CharSize(b)
			available := i
			if size > available {
				end = len(data) - i
			}
			break
		}
	}

	if end > 0 && !utf8.Valid(data[:end]) {
		for i := end - 1; i >= 0; i-- {
			if utf8.Valid(data[:i+1]) {
				return i + 1
			}
		}
		return 0
	}

	return end
}

func (p *UTF8StreamParser) utf8CharSize(b byte) int {
	if b&0x80 == 0 {
		return 1
	}
	if b&0xE0 == 0xC0 {
		return 2
	}
	if b&0xF0 == 0xE0 {
		return 3
	}
	if b&0xF8 == 0xF0 {
		return 4
	}
	return 1
}
