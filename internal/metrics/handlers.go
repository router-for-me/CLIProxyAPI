package metrics

import (
	"encoding/json"
	"net/http"
	"time"
)

// MetricsServer теперь использует интерфейс MetricsStore.
type MetricsServer struct {
	store MetricsStore
}

// Конструктор также принимает интерфейс MetricsStore.
func NewMetricsServer(store MetricsStore) *MetricsServer {
	return &MetricsServer{store: store}
}

func (ms *MetricsServer) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

type MetricsResponse struct {
	Totals     *Totals            `json:"totals"`
	ByModel    []ModelStats       `json:"by_model"`
	TimeSeries []TimeSeriesBucket `json:"timeseries"`
}

func (ms *MetricsServer) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	query := MetricsQuery{}

	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			query.From = &t
		}
	}

	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			query.To = &t
		}
	}

	if model := r.URL.Query().Get("model"); model != "" {
		query.Model = &model
	}

	totals, err := ms.store.GetTotals(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	byModel, err := ms.store.GetByModel(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	timeSeries, err := ms.store.GetTimeSeries(ctx, query, 1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	response := MetricsResponse{
		Totals:     totals,
		ByModel:    byModel,
		TimeSeries: timeSeries,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}