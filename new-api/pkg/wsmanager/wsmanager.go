package wsmanager

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

const (
	KindRealtime  = "realtime"
	KindResponses = "responses"

	defaultCloseReason = "channel disabled or deleted"
	redisChannel       = "new-api:wsmanager:channel-close"
)

type entry struct {
	id        uint64
	channelID int
	kind      string
	close     func(reason string)
}

type closeEvent struct {
	ChannelIDs []int  `json:"channel_ids"`
	Reason     string `json:"reason"`
	Origin     string `json:"origin"`
}

var (
	mu       sync.Mutex
	nextID   uint64
	registry = map[int]map[uint64]*entry{}

	originOnce sync.Once
	originID   string

	subscriberOnce sync.Once
)

func Register(channelID int, kind string, close func(reason string)) func() {
	if channelID <= 0 || close == nil {
		return func() {}
	}
	var closeOnce sync.Once
	safeClose := func(reason string) {
		closeOnce.Do(func() {
			close(reason)
		})
	}
	id := atomic.AddUint64(&nextID, 1)
	e := &entry{
		id:        id,
		channelID: channelID,
		kind:      kind,
		close:     safeClose,
	}

	mu.Lock()
	if registry[channelID] == nil {
		registry[channelID] = map[uint64]*entry{}
	}
	registry[channelID][id] = e
	mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			defer mu.Unlock()
			entries := registry[channelID]
			if entries == nil {
				return
			}
			delete(entries, id)
			if len(entries) == 0 {
				delete(registry, channelID)
			}
		})
	}
}

func CloseChannel(channelID int, reason string) int {
	return CloseChannels([]int{channelID}, reason)
}

func CloseChannels(channelIDs []int, reason string) int {
	reason = normalizeReason(reason)
	entries := takeEntries(channelIDs)
	for _, e := range entries {
		e.close(reason)
	}
	if len(entries) > 0 {
		common.SysLog(fmt.Sprintf("closed %d active websocket connection(s), channels=%v, kinds=%v, reason=%s", len(entries), entryChannelIDs(entries), entryKindCounts(entries), reason))
	}
	return len(entries)
}

func CloseChannelsAndBroadcast(channelIDs []int, reason string) int {
	count := CloseChannels(channelIDs, reason)
	if err := PublishCloseChannels(context.Background(), channelIDs, reason); err != nil {
		common.SysLog(fmt.Sprintf("failed to publish websocket close event: %v", err))
	}
	return count
}

func StartSubscriber(ctx context.Context) {
	rdb := common.RDB
	if !common.RedisEnabled || rdb == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	subscriberOnce.Do(func() {
		go func() {
			pubsub := rdb.Subscribe(ctx, channelCloseTopic(rdb.Options().DB))
			defer pubsub.Close()
			receiveChannelCloseEvents(ctx, pubsub.Channel(), getOriginID())
		}()
	})
}

func PublishCloseChannels(ctx context.Context, channelIDs []int, reason string) error {
	rdb := common.RDB
	if !common.RedisEnabled || rdb == nil {
		return nil
	}
	ids := uniqueChannelIDs(channelIDs)
	if len(ids) == 0 {
		return nil
	}
	payload, err := common.Marshal(closeEvent{
		ChannelIDs: ids,
		Reason:     normalizeReason(reason),
		Origin:     getOriginID(),
	})
	if err != nil {
		return err
	}
	return rdb.Publish(ctx, channelCloseTopic(rdb.Options().DB), payload).Err()
}

func channelCloseTopic(database int) string {
	// Redis Pub/Sub crosses logical databases, unlike the cache keys. Include
	// the selected database so independent deployments cannot close each other.
	return fmt.Sprintf("%s:db:%d", redisChannel, database)
}

func receiveChannelCloseEvents(ctx context.Context, ch <-chan *redis.Message, origin string) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var event closeEvent
			if err := common.Unmarshal([]byte(msg.Payload), &event); err != nil {
				common.SysLog(fmt.Sprintf("failed to unmarshal websocket close event: %v", err))
				continue
			}
			if event.Origin == origin {
				continue
			}
			CloseChannels(event.ChannelIDs, event.Reason)
		}
	}
}

func takeEntries(channelIDs []int) []*entry {
	ids := uniqueChannelIDs(channelIDs)
	if len(ids) == 0 {
		return nil
	}

	mu.Lock()
	defer mu.Unlock()

	var entries []*entry
	for _, channelID := range ids {
		for _, e := range registry[channelID] {
			entries = append(entries, e)
		}
		delete(registry, channelID)
	}
	return entries
}

func entryChannelIDs(entries []*entry) []int {
	ids := make([]int, 0, len(entries))
	for _, e := range entries {
		if e != nil {
			ids = append(ids, e.channelID)
		}
	}
	return uniqueChannelIDs(ids)
}

func entryKindCounts(entries []*entry) map[string]int {
	counts := make(map[string]int)
	for _, e := range entries {
		if e == nil {
			continue
		}
		counts[e.kind]++
	}
	return counts
}

func uniqueChannelIDs(channelIDs []int) []int {
	seen := make(map[int]struct{}, len(channelIDs))
	ids := make([]int, 0, len(channelIDs))
	for _, id := range channelIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func normalizeReason(reason string) string {
	if reason == "" {
		return defaultCloseReason
	}
	return reason
}

func getOriginID() string {
	originOnce.Do(func() {
		name := common.NodeName
		if name == "" {
			name = "node"
		}
		originID = fmt.Sprintf("%s-%d-%d", name, os.Getpid(), time.Now().UnixNano())
	})
	return originID
}
