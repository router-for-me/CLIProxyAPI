package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

const (
	maxInputPCMBytes  = 15 << 20
	maxOutputPCMBytes = 64 << 20
	wavHeaderSize     = 44
)

func readPCM16WAV(path string) ([]byte, error) {
	fileInfo, errStat := os.Stat(path)
	if errStat != nil {
		return nil, errStat
	}
	if fileInfo.Size() > maxInputPCMBytes+(1<<20) {
		return nil, fmt.Errorf("WAV file is too large: %d bytes", fileInfo.Size())
	}
	payload, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	if len(payload) < 12 || string(payload[:4]) != "RIFF" || string(payload[8:12]) != "WAVE" {
		return nil, errors.New("input is not a RIFF/WAVE file")
	}

	var formatFound bool
	var audioFormat uint16
	var channels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	var pcm bytes.Buffer
	for offset := 12; offset+8 <= len(payload); {
		chunkID := string(payload[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(payload[offset+4 : offset+8]))
		chunkStart := offset + 8
		chunkEnd := chunkStart + chunkSize
		if chunkSize < 0 || chunkEnd < chunkStart || chunkEnd > len(payload) {
			return nil, fmt.Errorf("invalid WAV %q chunk size", chunkID)
		}
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, errors.New("WAV fmt chunk is too short")
			}
			audioFormat = binary.LittleEndian.Uint16(payload[chunkStart : chunkStart+2])
			channels = binary.LittleEndian.Uint16(payload[chunkStart+2 : chunkStart+4])
			sampleRate = binary.LittleEndian.Uint32(payload[chunkStart+4 : chunkStart+8])
			bitsPerSample = binary.LittleEndian.Uint16(payload[chunkStart+14 : chunkStart+16])
			formatFound = true
		case "data":
			if pcm.Len()+chunkSize > maxInputPCMBytes {
				return nil, fmt.Errorf("WAV PCM data exceeds %d bytes", maxInputPCMBytes)
			}
			_, _ = pcm.Write(payload[chunkStart:chunkEnd])
		}
		offset = chunkEnd
		if chunkSize%2 != 0 {
			offset++
		}
	}
	if !formatFound {
		return nil, errors.New("WAV fmt chunk is missing")
	}
	if audioFormat != 1 {
		return nil, fmt.Errorf("WAV audio format must be PCM (1), got %d", audioFormat)
	}
	if channels != 1 {
		return nil, fmt.Errorf("WAV must be mono, got %d channels", channels)
	}
	if sampleRate != audioSampleRate {
		return nil, fmt.Errorf("WAV sample rate must be %d Hz, got %d Hz", audioSampleRate, sampleRate)
	}
	if bitsPerSample != 16 {
		return nil, fmt.Errorf("WAV must use 16-bit samples, got %d bits", bitsPerSample)
	}
	if pcm.Len() == 0 {
		return nil, errors.New("WAV data chunk is empty or missing")
	}
	if pcm.Len()%audioBytesPerSample != 0 {
		return nil, errors.New("WAV PCM data contains an incomplete sample")
	}
	return append([]byte(nil), pcm.Bytes()...), nil
}

func writePCM16WAV(path string, pcm []byte) error {
	if len(pcm) == 0 {
		return errors.New("cannot write an empty WAV response")
	}
	if len(pcm) > maxOutputPCMBytes {
		return fmt.Errorf("response PCM data exceeds %d bytes", maxOutputPCMBytes)
	}
	if len(pcm)%audioBytesPerSample != 0 {
		return errors.New("response PCM data contains an incomplete sample")
	}

	var payload bytes.Buffer
	payload.Grow(wavHeaderSize + len(pcm))
	writeString := func(value string) error {
		_, errWrite := payload.WriteString(value)
		return errWrite
	}
	writeValue := func(value any) error {
		return binary.Write(&payload, binary.LittleEndian, value)
	}
	if errWrite := writeString("RIFF"); errWrite != nil {
		return errWrite
	}
	if errWrite := writeValue(uint32(36 + len(pcm))); errWrite != nil {
		return errWrite
	}
	if errWrite := writeString("WAVEfmt "); errWrite != nil {
		return errWrite
	}
	for _, value := range []any{
		uint32(16),
		uint16(1),
		uint16(1),
		uint32(audioSampleRate),
		uint32(audioSampleRate * audioBytesPerSample),
		uint16(audioBytesPerSample),
		uint16(16),
	} {
		if errWrite := writeValue(value); errWrite != nil {
			return errWrite
		}
	}
	if errWrite := writeString("data"); errWrite != nil {
		return errWrite
	}
	if errWrite := writeValue(uint32(len(pcm))); errWrite != nil {
		return errWrite
	}
	if _, errWrite := payload.Write(pcm); errWrite != nil {
		return errWrite
	}
	if errWrite := os.WriteFile(path, payload.Bytes(), 0o644); errWrite != nil {
		return errWrite
	}
	return nil
}
