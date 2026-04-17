package helps

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const (
	StreamChunkBufferSize  = 32
	streamLineInitialSize  = 64 * 1024
	streamLineMaxSizeBytes = 8 * 1024 * 1024
)

var ErrStreamLineTooLong = errors.New("stream line exceeds configured maximum")

// ReadStreamLines reads newline-delimited records with a modest reader buffer while
// enforcing a hard per-line size cap. The handler receives stable line slices that
// do not alias the reader's internal buffer.
func ReadStreamLines(r io.Reader, handler func([]byte) error) error {
	reader := bufio.NewReaderSize(r, streamLineInitialSize)
	for {
		line, err := readLimitedLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if errHandle := handler(line); errHandle != nil {
			return errHandle
		}
	}
}

func readLimitedLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 && len(fragment) == 0 {
					return nil, io.EOF
				}
				line = append(line, fragment...)
				if len(line) > streamLineMaxSizeBytes {
					return nil, fmt.Errorf("%w: %d bytes", ErrStreamLineTooLong, len(line))
				}
				return line, nil
			}
			return nil, err
		}
		line = append(line, fragment...)
		if len(line) > streamLineMaxSizeBytes {
			return nil, fmt.Errorf("%w: %d bytes", ErrStreamLineTooLong, len(line))
		}
		if !isPrefix {
			return line, nil
		}
	}
}
