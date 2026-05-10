package resp

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Address       string
	Password      string
	QueueKey      string
	TLSSkipVerify bool
}

type Client struct{ opts Options }

func New(opts Options) *Client {
	if opts.QueueKey == "" {
		opts.QueueKey = "queue"
	}
	return &Client{opts: opts}
}

func AddressFromBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimPrefix(baseURL, "https://")
	baseURL = strings.TrimPrefix(baseURL, "http://")
	if idx := strings.Index(baseURL, "/"); idx >= 0 {
		baseURL = baseURL[:idx]
	}
	if baseURL == "" {
		return "127.0.0.1:8317"
	}
	return baseURL
}

func (c *Client) Pop(ctx context.Context, count int) ([][]byte, error) {
	if count <= 0 {
		count = 1
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.opts.Address)
	if err != nil {
		return nil, fmt.Errorf("dial usage queue: %w", err)
	}
	if c.opts.TLSSkipVerify {
		conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	}
	defer func() { _ = conn.Close() }()

	// Propagate context deadline to reads/writes; fall back to a 30s cap so a
	// context without a deadline never blocks the goroutine indefinitely.
	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set deadline on usage queue conn: %w", err)
	}

	reader := bufio.NewReader(conn)
	if err := writeArray(conn, "AUTH", c.opts.Password); err != nil {
		return nil, err
	}
	if _, err := readValue(reader); err != nil {
		return nil, fmt.Errorf("auth usage queue: %w", err)
	}
	if err := writeArray(conn, "RPOP", c.opts.QueueKey, strconv.Itoa(count)); err != nil {
		return nil, err
	}
	value, err := readValue(reader)
	if err != nil {
		return nil, fmt.Errorf("pop usage queue: %w", err)
	}
	items, ok := value.([][]byte)
	if !ok {
		if item, ok := value.([]byte); ok && item != nil {
			return [][]byte{item}, nil
		}
		return nil, nil
	}
	return items, nil
}

func writeArray(w io.Writer, parts ...string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(parts)); err != nil {
		return err
	}
	for _, part := range parts {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(part), part); err != nil {
			return err
		}
	}
	return nil
}

func readValue(r *bufio.Reader) (any, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return nil, fmt.Errorf("empty RESP line")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '$':
		length, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return []byte(nil), nil
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf[:length], nil
	case '*':
		count, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, count)
		for i := 0; i < count; i++ {
			value, err := readValue(r)
			if err != nil {
				return nil, err
			}
			if bytes, ok := value.([]byte); ok && bytes != nil {
				items = append(items, bytes)
			}
		}
		return items, nil
	case '-':
		return nil, fmt.Errorf("RESP error: %s", line[1:])
	default:
		return nil, fmt.Errorf("unsupported RESP line: %s", line)
	}
}
