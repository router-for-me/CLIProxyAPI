import { describe, expect, test } from 'bun:test';
import { parseLogLine } from '../src/features/logs/model/logParsing';
import {
  buildLogPathOptions,
  normalizeLogPathFilters,
} from '../src/features/logs/hooks/useLogFilters';

const prefix = '[2026-06-08 12:34:56] [a1b2c3d4] [info ] [gin_logger.go:110]';

test('normalizes previously persisted quoted path filters without dropping selections', () => {
  expect(normalizeLogPathFilters(['"/v1/responses"', '/v1/responses', '/rare'])).toEqual([
    '/v1/responses',
    '/rare',
  ]);
});

describe('parseLogLine backend formatter', () => {
  test('parses quoted paths, loopback IPv6, hour durations and retains credits/errors', () => {
    const raw = `${prefix} 200 | 1h2m3s | ::1 | POST "/v1/chat completions?q=a|b" [credits] | private failure`;
    expect(parseLogLine(raw)).toEqual({
      raw,
      timestamp: '2026-06-08 12:34:56',
      requestId: 'a1b2c3d4',
      level: 'info',
      source: 'gin_logger.go:110',
      statusCode: 200,
      latency: '1h2m3s',
      ip: '::1',
      method: 'POST',
      path: '/v1/chat completions?q=a|b',
      message: '[credits] | private failure',
    });
  });

  test.each(['23.559s', '1h0m0s', '2m3s', '500µs', '12ns', '0s'])(
    'supports Go duration %s',
    (duration) => {
      expect(parseLogLine(`${prefix} 200 | ${duration} | 127.0.0.1 | GET "/healthz"`).latency).toBe(
        duration
      );
    }
  );

  test.each(['::1', '::', '2001:db8::1', '2001:db8:0:0:0:0:0:1', '127.0.0.1'])(
    'keeps the full IP %s',
    (ip) => {
      expect(parseLogLine(`${prefix} 200 | 1ms | ${ip} | GET "/v1/models"`).ip).toBe(ip);
    }
  );

  test('does not confuse time of day with IPv6', () => {
    expect(parseLogLine('task ran at 12:34:56').ip).toBeUndefined();
  });

  test('maps panic to fatal without extending the existing level model', () => {
    const parsed = parseLogLine('[2026-06-08 12:34:56] [--------] [panic] stopped');
    expect(parsed.level).toBe('fatal');
    expect(parsed.requestId).toBeUndefined();
    expect(parsed.message).toBe('stopped');
  });

  test('preserves unknown bracket labels and mixed metadata text', () => {
    const message =
      '[credits] note | elapsed=1h2m3s extra | peer=::1 extra | request_id=abc extra | GET "/v1/models" [credits] custom';
    const parsed = parseLogLine(`${prefix} ${message}`);
    expect(parsed.message).toBe(
      '[credits] note | elapsed=1h2m3s extra | peer=::1 extra | request_id=abc extra | [credits] custom'
    );
    expect(parsed.raw).toBe(`${prefix} ${message}`);
  });

  test('preserves full named UUIDs', () => {
    const id = '12345678-1234-4321-abcd-123456789abc';
    expect(parseLogLine(`request_id=${id}`).requestId).toBe(id);
  });

  test('parses Gin timestamps without consuming unknown source labels', () => {
    const parsed = parseLogLine('[GIN] 2026/06/08 - 12:34:56 | 200 | 1ms | ::1 | GET "/v1/models"');
    expect(parsed.timestamp).toBe('2026-06-08 12:34:56');
    expect(parsed.source).toBeUndefined();
    expect(parsed.message).toBe('');
  });

  test('retains arbitrary non-request text mentioning HTTP methods', () => {
    expect(parseLogLine(`${prefix} operation GET failed | detail`).message).toBe(
      'operation GET failed | detail'
    );
  });
});

describe('log path filter options', () => {
  test('includes selected paths outside the popular twelve, with accurate counts', () => {
    const lines = Array.from({ length: 14 }, (_, index) =>
      parseLogLine(`${prefix} 200 | 1ms | ::1 | GET "/path/${String(index).padStart(2, '0')}"`)
    );
    const options = buildLogPathOptions(lines, ['/path/13', '/missing', '/path/00']);
    expect(options).toHaveLength(14);
    expect(options).toContainEqual({ path: '/path/13', count: 1 });
    expect(options).toContainEqual({ path: '/missing', count: 0 });
    expect(options.filter(({ path }) => path === '/path/00')).toHaveLength(1);
  });

  test('retains selected options when no logs remain', () => {
    expect(buildLogPathOptions([], ['/v1/models'])).toEqual([{ path: '/v1/models', count: 0 }]);
  });
});
