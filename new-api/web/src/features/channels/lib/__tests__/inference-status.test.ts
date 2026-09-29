/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, it } from 'vitest'

import {
  metricMean,
  metricValue,
  metricRatio,
  recentMetrics,
  type InferenceMetric,
  type InferenceStatus,
} from '../inference-status'

function snapshot(at: number, metrics: InferenceMetric[]): InferenceStatus {
  return {
    sampled_at: at,
    endpoints: { '/metrics': { status: 200 } },
    version: 'test',
    models: [],
    metrics,
    raw_metrics: '',
  }
}

describe('vLLM metric summaries', () => {
  it('preserves real zeroes and leaves missing or invalid metrics unavailable', () => {
    const metrics = [{ name: 'running', labels: {}, value: 0 }]
    expect(metricValue(metrics, 'running')).toBe(0)
    expect(metricValue(metrics, 'missing')).toBeUndefined()
    expect(
      metricValue([{ name: 'bad', labels: {}, value: Number.NaN }], 'bad')
    ).toBeUndefined()
    expect(metricRatio(0, 100)).toBe(0)
    expect(metricRatio(0, 0)).toBeUndefined()
  })

  it('sums engine counters but uses peak occupancy and weighted histogram means', () => {
    const metrics = [
      { name: 'running', labels: { engine: '0' }, value: 1 },
      { name: 'running', labels: { engine: '1' }, value: 3 },
      { name: 'latency_sum', labels: { engine: '0' }, value: 10 },
      { name: 'latency_count', labels: { engine: '0' }, value: 2 },
      { name: 'latency_sum', labels: { engine: '1' }, value: 50 },
      { name: 'latency_count', labels: { engine: '1' }, value: 8 },
    ]
    expect(metricValue(metrics, 'running')).toBe(4)
    expect(metricValue(metrics, 'running', {}, 'max')).toBe(3)
    expect(metricValue(metrics, 'running', { engine: '0' })).toBe(1)
    expect(metricMean(metrics, 'latency')).toBe(6)
  })

  it('computes counter deltas using elapsed sample time regardless of label order', () => {
    const previous = snapshot(1000, [
      {
        name: 'vllm:generation_tokens_total',
        labels: { model_name: 'm', engine: '0' },
        value: 100,
      },
    ])
    const current = snapshot(6000, [
      {
        name: 'vllm:generation_tokens_total',
        labels: { engine: '0', model_name: 'm' },
        value: 150,
      },
    ])
    const recent = recentMetrics(current, previous)
    expect(recent?.seconds).toBe(5)
    expect(
      metricRatio(
        metricValue(recent?.metrics ?? [], 'vllm:generation_tokens_total'),
        recent?.seconds
      )
    ).toBe(10)
    expect(recentMetrics(current)).toBeUndefined()
    expect(recentMetrics(previous, current)).toBeUndefined()
  })

  it('restarts the window when one engine resets even if the combined total increases', () => {
    const previous = snapshot(1000, [
      { name: 'vllm:prompt_tokens_total', labels: { engine: '0' }, value: 100 },
      { name: 'vllm:prompt_tokens_total', labels: { engine: '1' }, value: 100 },
    ])
    const current = snapshot(6000, [
      { name: 'vllm:prompt_tokens_total', labels: { engine: '0' }, value: 0 },
      { name: 'vllm:prompt_tokens_total', labels: { engine: '1' }, value: 300 },
    ])
    expect(recentMetrics(current, previous)).toBeUndefined()
    current.metrics[0].value = 30
    expect(
      recentMetrics(
        { ...current, sampled_at: 11000 },
        snapshot(6000, [
          {
            name: 'vllm:prompt_tokens_total',
            labels: { engine: '0' },
            value: 0,
          },
          {
            name: 'vllm:prompt_tokens_total',
            labels: { engine: '1' },
            value: 300,
          },
        ])
      )?.metrics[0].value
    ).toBe(30)
  })

  it('rejects restarted processes, changing series and unavailable metrics', () => {
    const previous = snapshot(1000, [
      { name: 'process_start_time_seconds', labels: {}, value: 1 },
    ])
    expect(
      recentMetrics(
        snapshot(6000, [
          { name: 'process_start_time_seconds', labels: {}, value: 2 },
        ]),
        previous
      )
    ).toBeUndefined()
    expect(recentMetrics(snapshot(6000, []), previous)).toBeUndefined()
    expect(
      recentMetrics(
        {
          ...previous,
          sampled_at: 6000,
          endpoints: { '/metrics': { status: 404, error: 'http_error' } },
        },
        previous
      )
    ).toBeUndefined()
  })
})
