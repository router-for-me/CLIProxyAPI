package helps

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	StreamChunkBufferSize  = 32
	streamLineInitialSize  = 4 * 1024
	streamLineMaxSizeBytes = 8 * 1024 * 1024
)

var ErrStreamLineTooLong = errors.New("stream line exceeds configured maximum")

var streamReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReaderSize(nil, streamLineInitialSize)
	},
}

// ReadStreamLines reads newline-delimited records with a modest reader buffer while
// enforcing a hard per-line size cap. The handler receives stable line slices that
// do not alias the reader's internal buffer.
func ReadStreamLines(r io.Reader, handler func([]byte) error) error {
	reader := streamReaderPool.Get().(*bufio.Reader)
	reader.Reset(r)
	defer func() {
		reader.Reset(nil)
		streamReaderPool.Put(reader)
	}()

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
