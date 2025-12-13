package metrics

import (
	"net/http"
)

func (ms *MetricsServer) HandleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(metricsUIHTML))
}

const metricsUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>CLI Proxy Metrics</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.0"></script>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
            background: #0f172a;
            color: #e2e8f0;
            padding: 2rem;
        }
        .container { max-width: 1400px; margin: 0 auto; }
        h1 { font-size: 2rem; margin-bottom: 2rem; color: #38bdf8; }
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 1.5rem;
            margin-bottom: 2rem;
        }
        .stat-card {
            background: #1e293b;
            padding: 1.5rem;
            border-radius: 12px;
            border: 1px solid #334155;
        }
        .stat-label {
            font-size: 0.875rem;
            color: #94a3b8;
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }
        .stat-value {
            font-size: 2rem;
            font-weight: 700;
            margin-top: 0.5rem;
            color: #38bdf8;
        }
        .chart-container {
            background: #1e293b;
            padding: 2rem;
            border-radius: 12px;
            border: 1px solid #334155;
            margin-bottom: 2rem;
        }
        .models-table {
            background: #1e293b;
            border-radius: 12px;
            border: 1px solid #334155;
            overflow: hidden;
        }
        table {
            width: 100%;
            border-collapse: collapse;
        }
        th, td {
            padding: 1rem;
            text-align: left;
            border-bottom: 1px solid #334155;
        }
        th {
            background: #0f172a;
            color: #94a3b8;
            font-weight: 600;
            text-transform: uppercase;
            font-size: 0.75rem;
            letter-spacing: 0.05em;
        }
        tr:last-child td { border-bottom: none; }
        .loading {
            text-align: center;
            padding: 4rem;
            color: #64748b;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>📊 CLI Proxy Metrics</h1>
        
        <div class="stats-grid">
            <div class="stat-card">
                <div class="stat-label">Total Requests</div>
                <div class="stat-value" id="totalRequests">-</div>
            </div>
            <div class="stat-card">
                <div class="stat-label">Total Tokens</div>
                <div class="stat-value" id="totalTokens">-</div>
            </div>
            <div class="stat-card">
                <div class="stat-label">Active Models</div>
                <div class="stat-value" id="activeModels">-</div>
            </div>
            <div class="stat-card">
                <div class="stat-label">Last 24h Requests</div>
                <div class="stat-value" id="last24hRequests">-</div>
            </div>
        </div>

        <div class="chart-container">
            <canvas id="timeSeriesChart"></canvas>
        </div>

        <div class="models-table">
            <table>
                <thead>
                    <tr>
                        <th>Model</th>
                        <th>Requests</th>
                        <th>Tokens</th>
                        <th>Avg Latency (ms)</th>
                    </tr>
                </thead>
                <tbody id="modelsTableBody">
                    <tr><td colspan="4" class="loading">Loading...</td></tr>
                </tbody>
            </table>
        </div>
    </div>

    <script>
        const API_BASE = '';
        
        async function fetchMetrics() {
            try {
                const response = await fetch(API_BASE + '/_qs/metrics');
                return await response.json();
            } catch (error) {
                console.error('Failed to fetch metrics:', error);
                return null;
            }
        }

        function formatNumber(num) {
            return new Intl.NumberFormat().format(num);
        }

        function updateStats(data) {
            document.getElementById('totalRequests').textContent = 
                formatNumber(data.totals.total_requests);
            document.getElementById('totalTokens').textContent = 
                formatNumber(data.totals.total_tokens);
            document.getElementById('activeModels').textContent = 
                data.by_model.length;
            
            const last24h = data.timeseries.reduce((sum, b) => sum + b.requests, 0);
            document.getElementById('last24hRequests').textContent = 
                formatNumber(last24h);
        }

        function updateModelsTable(models) {
            const tbody = document.getElementById('modelsTableBody');
            if (!models || models.length === 0) {
                tbody.innerHTML = '<tr><td colspan="4" class="loading">No data</td></tr>';
                return;
            }
            
            tbody.innerHTML = models.map(m => ` + "`" + `
                <tr>
                    <td><strong>${m.model}</strong></td>
                    <td>${formatNumber(m.requests)}</td>
                    <td>${formatNumber(m.tokens)}</td>
                    <td>${m.avg_latency_ms.toFixed(0)}</td>
                </tr>
            ` + "`" + `).join('');
        }

        let chart = null;
        function updateChart(timeseries) {
            const ctx = document.getElementById('timeSeriesChart');
            
            if (chart) chart.destroy();
            
            chart = new Chart(ctx, {
                type: 'line',
                data: {
                    labels: timeseries.map(b => 
                        new Date(b.bucket_start).toLocaleTimeString('en-US', {
                            hour: '2-digit',
                            minute: '2-digit'
                        })
                    ),
                    datasets: [{
                        label: 'Requests per Hour',
                        data: timeseries.map(b => b.requests),
                        borderColor: '#38bdf8',
                        backgroundColor: 'rgba(56, 189, 248, 0.1)',
                        fill: true,
                        tension: 0.4
                    }]
                },
                options: {
                    responsive: true,
                    plugins: {
                        legend: { display: false },
                        title: {
                            display: true,
                            text: 'Usage Over Last 24 Hours',
                            color: '#e2e8f0',
                            font: { size: 16 }
                        }
                    },
                    scales: {
                        y: {
                            beginAtZero: true,
                            ticks: { color: '#94a3b8' },
                            grid: { color: '#334155' }
                        },
                        x: {
                            ticks: { color: '#94a3b8' },
                            grid: { color: '#334155' }
                        }
                    }
                }
            });
        }

        async function refreshData() {
            const data = await fetchMetrics();
            if (data) {
                updateStats(data);
                updateModelsTable(data.by_model);
                updateChart(data.timeseries);
            }
        }

        refreshData();
        setInterval(refreshData, 30000);
    </script>
</body>
</html>` + "`"