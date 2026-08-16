package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// stubMinioClient is an in-memory fake implementing minioClient.
type stubMinioClient struct {
	mu      sync.Mutex
	objects map[string][]byte
	times   map[string]time.Time
}

func newStubMinioClient() *stubMinioClient {
	return &stubMinioClient{objects: map[string][]byte{}, times: map[string]time.Time{}}
}

func (s *stubMinioClient) PutObject(ctx context.Context, bucket, object string, reader io.Reader, _ int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return minio.UploadInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[object] = data
	s.times[object] = time.Now()
	return minio.UploadInfo{Key: object, Size: int64(len(data))}, nil
}

func (s *stubMinioClient) ListObjects(ctx context.Context, bucket string, opts minio.ListObjectsOptions) <-chan minio.ObjectInfo {
	out := make(chan minio.ObjectInfo, 16)
	go func() {
		defer close(out)
		s.mu.Lock()
		defer s.mu.Unlock()
		prefix := opts.Prefix
		keys := make([]string, 0, len(s.objects))
		for k := range s.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			out <- minio.ObjectInfo{Key: k, Size: int64(len(s.objects[k])), LastModified: s.times[k]}
		}
	}()
	return out
}

func (s *stubMinioClient) RemoveObject(ctx context.Context, bucket, object string, _ minio.RemoveObjectOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, object)
	delete(s.times, object)
	return nil
}

// stubObjectGetter implements objectGetter against the same in-memory store.
type stubObjectGetter struct {
	stub *stubMinioClient
}

func (g stubObjectGetter) Get(ctx context.Context, bucket, object string) (io.ReadCloser, error) {
	g.stub.mu.Lock()
	defer g.stub.mu.Unlock()
	data, ok := g.stub.objects[object]
	if !ok {
		return nil, errors.New("stub: not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func newTestClient(stub *stubMinioClient) *S3Client {
	return &S3Client{client: stub, getter: stubObjectGetter{stub: stub}, bucket: "test-bucket", prefix: "bk"}
}

func TestS3ClientListAndDelete(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)

	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("snap_%02d.json", i)
		if _, err := cli.Upload(ctx, key, []byte("x"), "application/json"); err != nil {
			t.Fatalf("upload %s: %v", key, err)
		}
		// Stagger the times so sort is deterministic.
		stub.mu.Lock()
		stub.times["bk/"+key] = now.Add(time.Duration(i) * time.Second)
		stub.mu.Unlock()
	}

	snaps, err := cli.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 5 {
		t.Fatalf("want 5 snapshots, got %d", len(snaps))
	}
	// Sorted newest first.
	if snaps[0].Key != "bk/snap_04.json" {
		t.Errorf("newest-first mismatch: got %s", snaps[0].Key)
	}

	if err := cli.Delete(ctx, "snap_04.json"); err != nil {
		t.Fatal(err)
	}
	snaps, err = cli.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 4 {
		t.Fatalf("want 4 after delete, got %d", len(snaps))
	}
}

func TestS3ClientDownload(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)

	ctx := context.Background()
	data := []byte(`{"hello":"world"}`)
	if _, err := cli.Upload(ctx, "snap.json", data, "application/json"); err != nil {
		t.Fatal(err)
	}
	got, err := cli.Download(ctx, "snap.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("download mismatch: got %q want %q", got, data)
	}
	if _, err := cli.Download(ctx, "missing.json"); err == nil {
		t.Error("expected error downloading missing key")
	}
}

func TestS3ClientPrefixing(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)

	ctx := context.Background()
	full, err := cli.Upload(ctx, "/snap.json", []byte("x"), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if full != "bk/snap.json" {
		t.Errorf("prefixed key mismatch: got %s", full)
	}

	// Empty prefix: key passes through unchanged.
	bare := &S3Client{client: stub, getter: stubObjectGetter{stub: stub}, bucket: "test-bucket"}
	full, err = bare.Upload(ctx, "snap.json", []byte("x"), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if full != "snap.json" {
		t.Errorf("unprefixed key mismatch: got %s", full)
	}
}

func TestS3ClientDeleteAcceptsPrefixedKey(t *testing.T) {
	stub := newStubMinioClient()
	cli := newTestClient(stub)
	ctx := context.Background()

	if _, err := cli.Upload(ctx, "snap.json", []byte("x"), "application/json"); err != nil {
		t.Fatal(err)
	}
	snaps, err := cli.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("setup: want 1 snapshot, got %d", len(snaps))
	}
	// Delete the key exactly as returned by List (already carries the prefix).
	if err := cli.Delete(ctx, snaps[0].Key); err != nil {
		t.Fatal(err)
	}
	snaps, err = cli.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected object gone after delete with prefixed key, got %d remaining", len(snaps))
	}
}

func TestNewS3ClientValidation(t *testing.T) {
	if _, err := NewS3Client(S3Config{Bucket: "b"}); err == nil {
		t.Error("expected error for empty endpoint")
	}
	if _, err := NewS3Client(S3Config{Endpoint: "e"}); err == nil {
		t.Error("expected error for empty bucket")
	}
	c, err := NewS3Client(S3Config{Endpoint: " localhost:9000 ", Bucket: " b ", Prefix: "/bk/"})
	if err != nil {
		t.Fatal(err)
	}
	if c.bucket != "b" || c.prefix != "bk" {
		t.Errorf("config not trimmed: bucket=%q prefix=%q", c.bucket, c.prefix)
	}
}
