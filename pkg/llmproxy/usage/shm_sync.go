package usage

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/edsrzf/mmap-go"
)

const (
	MaxProviders      = 32
	ProviderSlotSize  = 128
	ProviderOffset    = 256 * 256
	ShmSize           = ProviderOffset + (MaxProviders * ProviderSlotSize) + 8192
)

// SyncToSHM writes the current provider metrics to the shared memory mesh.
func SyncToSHM(shmPath string) error {
	metrics := GetProviderMetrics()

	f, err := os.OpenFile(shmPath, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return fmt.Errorf("failed to open SHM: %w", err)
	}
	defer f.Close()

	// Ensure file is large enough
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < int64(ShmSize) {
		if err := f.Truncate(int64(ShmSize)); err != nil {
			return err
		}
	}

	m, err := mmap.Map(f, mmap.RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to mmap: %w", err)
	}
	defer m.Unmap()

	now := float64(time.Now().UnixNano()) / 1e9

	for name, data := range metrics {
		if name == "" {
			continue
		}
		
		nameBytes := make([]byte, 32)
		copy(nameBytes, name)

		var targetIdx = -1
		for i := 0; i < MaxProviders; i++ {
			start := ProviderOffset + (i * ProviderSlotSize)
			slotName := m[start : start+32]
			if slotName[0] == 0 {
				if targetIdx == -1 {
					targetIdx = i
				}
				continue
			}
			if string(slotName[:len(name)]) == name {
				targetIdx = i
				break
			}
		}

		if targetIdx == -1 {
			continue // No slots left
		}

		start := ProviderOffset + (targetIdx * ProviderSlotSize)
		copy(m[start:start+32], nameBytes)
		binary.LittleEndian.PutUint64(m[start+32:start+40], uint64(data.RequestCount))
		binary.LittleEndian.PutUint64(m[start+40:start+48], uint64(data.SuccessCount))
		binary.LittleEndian.PutUint64(m[start+48:start+56], uint64(data.FailureCount))
		binary.LittleEndian.PutUint32(m[start+56:start+60], uint32(data.LatencyP50Ms))
		binary.LittleEndian.PutUint32(m[start+60:start+64], math.Float32bits(float32(data.SuccessRate)))
		binary.LittleEndian.PutUint64(m[start+64:start+72], math.Float64bits(now))
	}

	return nil
}
