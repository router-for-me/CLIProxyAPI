// internal/metrics/metrics.go

package metrics

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsageMetric представляет собой запись о единственном вызове API
// с информацией об использовании токенов и задержке.
type UsageMetric struct {
	Timestamp        time.Time // Время завершения запроса
	Model            string    // Имя используемой модели
	PromptTokens     int       // Количество токенов во входном запросе
	CompletionTokens int       // Количество сгенерированных токенов в ответе
	TotalTokens      int       // Общее количество токенов
	RequestID        string    // Уникальный идентификатор запроса
	Status           string    // HTTP-статус (например, "200", "500")
	LatencyMs        int64     // Задержка запроса в миллисекундах
	APIKeyHash       string    // SHA-256 хеш ключа API (для анонимизации)
}

// MetricsStore определяет интерфейс для системы хранения и записи метрик.
// Теперь включает методы для чтения статистики, используемые в агрегаторе и хендлерах.
type MetricsStore interface {
	// Запись
	RecordUsage(ctx context.Context, metric UsageMetric) error
	Close() error

	// Чтение (определены в aggregator.go, но должны быть в интерфейсе)
	GetTotals(ctx context.Context, q MetricsQuery) (*Totals, error)
	GetByModel(ctx context.Context, q MetricsQuery) ([]ModelStats, error)
	GetTimeSeries(ctx context.Context, q MetricsQuery, bucketHours int) ([]TimeSeriesBucket, error)
}

// ====================================================================
// Глобальный механизм (для main.go)
// ====================================================================

// globalMetricsStore содержит ссылку на текущее активное хранилище метрик.
var globalMetricsStore MetricsStore

// GetGlobalMetricsStore возвращает текущее глобальное хранилище метрик.
func GetGlobalMetricsStore() MetricsStore {
	return globalMetricsStore
}

// SetGlobalMetricsStore устанавливает глобальное хранилище метрик.
func SetGlobalMetricsStore(store MetricsStore) {
	globalMetricsStore = store
	log.Debug("Global Metrics Store initialized.")
}