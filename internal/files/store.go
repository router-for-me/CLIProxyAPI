package files

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	pdf "rsc.io/pdf"
)

const (
	filesDirName    = "files"
	metadataExt     = ".meta.json"
	maxInlineSize   = 20 << 20 // 20 MiB pragmatic MVP guardrail for text extraction
	defaultFilePerm = 0o600
	defaultDirPerm  = 0o700
)

var (
	ErrNotFound          = errors.New("file not found")
	ErrUnsupportedFormat = errors.New("unsupported file format")
)

type Store struct {
	baseDir string
}

type Metadata struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose,omitempty"`
	Status    string `json:"status,omitempty"`

	ContentType string `json:"-"`
	Path        string `json:"-"`
}

type storedMetadata struct {
	Metadata
	StoredName string `json:"stored_name"`
}

func NewStore(authDir string) (*Store, error) {
	return NewStoreWithDir(authDir, "")
}

func NewStoreWithDir(authDir, filesDir string) (*Store, error) {
	resolved, err := util.ResolveAuthDir(authDir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(resolved) == "" {
		return nil, fmt.Errorf("auth-dir is empty")
	}
	baseDir := strings.TrimSpace(filesDir)
	if baseDir == "" {
		baseDir = filepath.Join(resolved, filesDirName)
	} else {
		baseDir, err = util.ResolveAuthDir(baseDir)
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(baseDir, defaultDirPerm); err != nil {
		return nil, fmt.Errorf("create files dir: %w", err)
	}
	return &Store{baseDir: baseDir}, nil
}

func (s *Store) Create(filename, purpose, contentType string, data []byte) (*Metadata, error) {
	if s == nil {
		return nil, fmt.Errorf("file store unavailable")
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "." || filename == "" {
		filename = "upload"
	}
	id := "file-" + uuid.NewString()
	storedName := id + filepath.Ext(filename)
	contentPath := filepath.Join(s.baseDir, storedName)
	metaPath := filepath.Join(s.baseDir, id+metadataExt)
	if err := os.WriteFile(contentPath, data, defaultFilePerm); err != nil {
		return nil, fmt.Errorf("write content: %w", err)
	}
	if contentType == "" {
		contentType = detectContentType(filename, data)
	}
	meta := storedMetadata{
		Metadata: Metadata{
			ID:          id,
			Object:      "file",
			Bytes:       int64(len(data)),
			CreatedAt:   time.Now().Unix(),
			Filename:    filename,
			Purpose:     strings.TrimSpace(purpose),
			Status:      "processed",
			ContentType: contentType,
			Path:        contentPath,
		},
		StoredName: storedName,
	}
	blob, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		_ = os.Remove(contentPath)
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, blob, defaultFilePerm); err != nil {
		_ = os.Remove(contentPath)
		return nil, fmt.Errorf("write metadata: %w", err)
	}
	return &meta.Metadata, nil
}

func (s *Store) Get(id string) (*Metadata, error) {
	meta, _, err := s.load(id)
	if err != nil {
		return nil, err
	}
	return &meta.Metadata, nil
}

func (s *Store) Load(id string) (*Metadata, []byte, error) {
	meta, _, err := s.load(id)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(meta.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("read content: %w", err)
	}
	return &meta.Metadata, data, nil
}

func (s *Store) Delete(id string) error {
	meta, metaPath, err := s.load(id)
	if err != nil {
		return err
	}
	if err := os.Remove(meta.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete content: %w", err)
	}
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete metadata: %w", err)
	}
	return nil
}

func (s *Store) List() ([]Metadata, error) {
	if s == nil {
		return nil, fmt.Errorf("file store unavailable")
	}
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("read files dir: %w", err)
	}
	out := make([]Metadata, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), metadataExt) {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(s.baseDir, entry.Name()))
		if err != nil {
			continue
		}
		var meta storedMetadata
		if err := json.Unmarshal(blob, &meta); err != nil {
			continue
		}
		meta.Path = filepath.Join(s.baseDir, meta.StoredName)
		out = append(out, meta.Metadata)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (s *Store) load(id string) (*storedMetadata, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("file store unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", ErrNotFound
	}
	metaPath := filepath.Join(s.baseDir, filepath.Base(id)+metadataExt)
	blob, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("read metadata: %w", err)
	}
	var meta storedMetadata
	if err := json.Unmarshal(blob, &meta); err != nil {
		return nil, "", fmt.Errorf("decode metadata: %w", err)
	}
	meta.Path = filepath.Join(s.baseDir, meta.StoredName)
	if meta.ContentType == "" {
		meta.ContentType = detectContentType(meta.Filename, nil)
	}
	return &meta, metaPath, nil
}

func detectContentType(filename string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

func ExtractText(meta *Metadata, data []byte) (string, error) {
	if meta == nil {
		return "", fmt.Errorf("metadata is required")
	}
	if len(data) > maxInlineSize {
		return "", fmt.Errorf("file too large for MVP text extraction: %d bytes", len(data))
	}
	name := strings.ToLower(meta.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".markdown", ".json", ".yaml", ".yml", ".csv", ".log", ".xml", ".html", ".htm", ".go", ".js", ".ts", ".jsx", ".tsx", ".py", ".java", ".c", ".cc", ".cpp", ".h", ".hpp", ".rs", ".sh", ".sql":
		return string(data), nil
	case ".pdf":
		return extractPDFText(data)
	case ".docx":
		return extractDOCXText(data)
	default:
		if strings.HasPrefix(strings.ToLower(meta.ContentType), "text/") || meta.ContentType == "application/json" {
			return string(data), nil
		}
		return "", fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

func extractDOCXText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open docx: %w", err)
	}
	var document *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			document = f
			break
		}
	}
	if document == nil {
		return "", fmt.Errorf("docx missing word/document.xml")
	}
	rc, err := document.Open()
	if err != nil {
		return "", fmt.Errorf("open document.xml: %w", err)
	}
	defer rc.Close()
	decoder := xml.NewDecoder(rc)
	var b strings.Builder
	for {
		tok, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("parse document.xml: %w", err)
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "t":
				var text string
				if err := decoder.DecodeElement(&text, &se); err != nil {
					return "", fmt.Errorf("decode docx text: %w", err)
				}
				b.WriteString(text)
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "p":
				b.WriteString("\n\n")
			case "tr":
				b.WriteByte('\n')
			case "tc":
				b.WriteByte('\t')
			}
		}
	}
	return strings.TrimSpace(normalizeExtractedText(b.String())), nil
}

func extractPDFText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		content := page.Content()
		lastY := 0.0
		for idx, text := range content.Text {
			if idx > 0 {
				delta := text.Y - lastY
				if delta < -2 || delta > 2 {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
			}
			b.WriteString(strings.TrimSpace(text.S))
			lastY = text.Y
		}
		if i < r.NumPage() {
			b.WriteString("\n\n")
		}
	}
	text := strings.TrimSpace(normalizeExtractedText(b.String()))
	if text == "" {
		return "", fmt.Errorf("pdf text extraction produced empty output")
	}
	return text, nil
}

func normalizeExtractedText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}
