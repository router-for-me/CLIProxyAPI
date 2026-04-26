package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/maphash"
	"strconv"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const (
	codexFinalUpstreamBodyMemoMaxEntries = 256
	codexPromptResolutionMemoMaxEntries  = 512
)

var (
	codexMemoHashSeed                 = maphash.MakeSeed()
	globalCodexFinalUpstreamBodyMemo  codexFinalUpstreamBodyMemo
	globalCodexFinalUpstreamBodyGroup helps.InFlightGroup[[]byte]
	globalCodexPromptResolutionMemo   codexPromptResolutionMemo
	globalCodexPromptResolutionGroup  helps.InFlightGroup[codexPromptCacheResolution]
)

type codexFinalUpstreamBodyMemoEntry struct {
	baseModel string
	opts      codexFinalUpstreamBodyOptions
	input     []byte
	output    []byte
}

type codexFinalUpstreamBodyMemo struct {
	mu      sync.RWMutex
	entries map[uint64]codexFinalUpstreamBodyMemoEntry
	order   []uint64
	next    int
}

func (m *codexFinalUpstreamBodyMemo) get(baseModel string, opts codexFinalUpstreamBodyOptions, input []byte) []byte {
	if m == nil || len(input) == 0 {
		return nil
	}
	hash := hashCodexFinalUpstreamBodyMemoKey(baseModel, opts, input)

	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[hash]
	if !ok || entry.baseModel != baseModel || entry.opts != opts || !bytes.Equal(entry.input, input) {
		return nil
	}
	return bytes.Clone(entry.output)
}

func (m *codexFinalUpstreamBodyMemo) set(baseModel string, opts codexFinalUpstreamBodyOptions, input []byte, output []byte) {
	if m == nil || len(input) == 0 || len(output) == 0 {
		return
	}
	hash := hashCodexFinalUpstreamBodyMemoKey(baseModel, opts, input)
	entry := codexFinalUpstreamBodyMemoEntry{
		baseModel: baseModel,
		opts:      opts,
		input:     bytes.Clone(input),
		output:    bytes.Clone(output),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[uint64]codexFinalUpstreamBodyMemoEntry, codexFinalUpstreamBodyMemoMaxEntries)
	}
	if _, exists := m.entries[hash]; !exists {
		m.record(hash, codexFinalUpstreamBodyMemoMaxEntries)
	}
	m.entries[hash] = entry
}

type codexPromptResolutionMemoEntry struct {
	from               sdktranslator.Format
	model              string
	scope              string
	payload            []byte
	executionSessionID string
	resolution         codexPromptCacheResolution
}

type codexPromptResolutionMemo struct {
	mu      sync.RWMutex
	entries map[uint64]codexPromptResolutionMemoEntry
	order   []uint64
	next    int
}

func (m *codexPromptResolutionMemo) get(from sdktranslator.Format, model string, scope string, executionSessionID string, payload []byte) (codexPromptCacheResolution, bool) {
	if m == nil {
		return codexPromptCacheResolution{}, false
	}
	hash := hashCodexPromptResolutionMemoKey(from, model, scope, executionSessionID, payload)

	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[hash]
	if !ok ||
		entry.from != from ||
		entry.model != model ||
		entry.scope != scope ||
		entry.executionSessionID != executionSessionID ||
		!bytes.Equal(entry.payload, payload) {
		return codexPromptCacheResolution{}, false
	}
	return entry.resolution, true
}

func (m *codexPromptResolutionMemo) set(from sdktranslator.Format, model string, scope string, executionSessionID string, payload []byte, resolution codexPromptCacheResolution) {
	if m == nil {
		return
	}
	hash := hashCodexPromptResolutionMemoKey(from, model, scope, executionSessionID, payload)
	entry := codexPromptResolutionMemoEntry{
		from:               from,
		model:              model,
		scope:              scope,
		payload:            bytes.Clone(payload),
		executionSessionID: executionSessionID,
		resolution:         resolution,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[uint64]codexPromptResolutionMemoEntry, codexPromptResolutionMemoMaxEntries)
	}
	if _, exists := m.entries[hash]; !exists {
		m.record(hash, codexPromptResolutionMemoMaxEntries)
	}
	m.entries[hash] = entry
}

func (m *codexFinalUpstreamBodyMemo) record(hash uint64, maxEntries int) {
	recordCodexMemoHash(&m.order, &m.next, m.entries, hash, maxEntries)
}

func (m *codexPromptResolutionMemo) record(hash uint64, maxEntries int) {
	recordCodexMemoHash(&m.order, &m.next, m.entries, hash, maxEntries)
}

func recordCodexMemoHash[T any](order *[]uint64, next *int, entries map[uint64]T, hash uint64, maxEntries int) {
	if maxEntries <= 0 {
		return
	}
	if len(*order) < maxEntries {
		*order = append(*order, hash)
		return
	}
	if len(*order) == 0 {
		return
	}
	old := (*order)[*next]
	delete(entries, old)
	(*order)[*next] = hash
	*next = (*next + 1) % len(*order)
}

func normalizeCodexFinalUpstreamBody(body []byte, baseModel string, auth *cliproxyauth.Auth, opts codexFinalUpstreamBodyOptions) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	if cached := globalCodexFinalUpstreamBodyMemo.get(baseModel, opts, body); cached != nil {
		return cached
	}

	key := codexFinalUpstreamBodyInflightKey(baseModel, opts, body)
	normalized, _, _, err := globalCodexFinalUpstreamBodyGroup.Do(context.Background(), key, func() ([]byte, error) {
		if cached := globalCodexFinalUpstreamBodyMemo.get(baseModel, opts, body); cached != nil {
			return cached, nil
		}
		out := normalizeCodexFinalUpstreamBodyUncached(body, baseModel, auth, opts)
		globalCodexFinalUpstreamBodyMemo.set(baseModel, opts, body, out)
		return bytes.Clone(out), nil
	})
	if err != nil {
		return normalizeCodexFinalUpstreamBodyUncached(body, baseModel, auth, opts)
	}
	return normalized
}

func codexFinalUpstreamBodyInflightKey(baseModel string, opts codexFinalUpstreamBodyOptions, input []byte) string {
	sum := sha256.Sum256(input)
	var encoded [sha256.Size * 2]byte
	hex.Encode(encoded[:], sum[:])

	buf := make([]byte, 0, len(baseModel)+96)
	buf = append(buf, baseModel...)
	buf = append(buf, '|')
	buf = strconv.AppendUint(buf, uint64(opts.requestKind), 10)
	buf = append(buf, '|')
	buf = strconv.AppendUint(buf, uint64(opts.streamMode), 10)
	buf = append(buf, '|')
	buf = strconv.AppendBool(buf, opts.preservePreviousResponseID)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(len(input)), 10)
	buf = append(buf, '|')
	buf = append(buf, encoded[:]...)
	return string(buf)
}

func hashCodexFinalUpstreamBodyMemoKey(baseModel string, opts codexFinalUpstreamBodyOptions, input []byte) uint64 {
	var h maphash.Hash
	h.SetSeed(codexMemoHashSeed)
	_, _ = h.WriteString(baseModel)
	_, _ = h.Write([]byte{byte(opts.requestKind), byte(opts.streamMode), boolToByte(opts.preservePreviousResponseID)})
	_, _ = h.Write(input)
	return h.Sum64()
}

func hashCodexPromptResolutionMemoKey(from sdktranslator.Format, model string, scope string, executionSessionID string, payload []byte) uint64 {
	var h maphash.Hash
	h.SetSeed(codexMemoHashSeed)
	_, _ = h.WriteString(string(from))
	_, _ = h.WriteString(model)
	_, _ = h.WriteString(scope)
	_, _ = h.WriteString(executionSessionID)
	if len(payload) > 0 {
		_, _ = h.Write(payload)
	}
	return h.Sum64()
}

func boolToByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func promptResolutionMemoInflightKey(from sdktranslator.Format, model string, scope string, executionSessionID string, payload []byte) string {
	hash := hashCodexPromptResolutionMemoKey(from, model, scope, executionSessionID, payload)
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, hash)
	return string(from) + "|" + model + "|" + scope + "|" + executionSessionID + "|" + string(buf)
}
