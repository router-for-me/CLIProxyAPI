package auth

import (
	"crypto/rand"
	"encoding/binary"
	"hash/fnv"
	"sync"
	"time"
)

var (
	fillFirstShuffleSeedOnce  sync.Once
	fillFirstShuffleSeedValue uint64
)

func fillFirstShuffleSeed() uint64 {
	fillFirstShuffleSeedOnce.Do(func() {
		fillFirstShuffleSeedValue = initFillFirstShuffleSeed()
	})
	return fillFirstShuffleSeedValue
}

func initFillFirstShuffleSeed() uint64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		seed := binary.LittleEndian.Uint64(buf[:])
		if seed != 0 {
			return seed
		}
	}

	seed := uint64(time.Now().UnixNano())
	if seed == 0 {
		seed = 1
	}
	return seed
}

func fillFirstShuffleRank(seed uint64, authID string) uint64 {
	h := fnv.New64a()
	var seedBuf [8]byte
	binary.LittleEndian.PutUint64(seedBuf[:], seed)
	_, _ = h.Write(seedBuf[:])
	_, _ = h.Write([]byte(authID))
	return h.Sum64()
}
