import { describe, expect, test } from 'bun:test';
import { generateSecureApiKey } from '../src/utils/apiKey';
import {
  API_KEY_STRENGTH_SEGMENTS,
  evaluateApiKeyStrength,
  type ApiKeyStrengthTier,
} from '../src/utils/apiKeyStrength';

const TIER_ORDER: ApiKeyStrengthTier[] = ['weak', 'fair', 'good', 'strong'];

describe('API key strength', () => {
  test('empty input lights no segment', () => {
    for (const value of ['', '   ']) {
      expect(evaluateApiKeyStrength(value)).toEqual({ tier: 'weak', segments: 0, bits: 0 });
    }
  });

  test('segments track the tier index', () => {
    const samples = ['a', 'Tr0ub4dor', 'Xk7#mQ2vLp9$Rn4wZt6c', generateSecureApiKey()];

    for (const sample of samples) {
      const { tier, segments } = evaluateApiKeyStrength(sample);
      expect(segments).toBe(TIER_ORDER.indexOf(tier) + 1);
      expect(segments).toBeLessThanOrEqual(API_KEY_STRENGTH_SEGMENTS);
    }
  });

  test('generated keys always reach the top tier', () => {
    for (let i = 0; i < 50; i += 1) {
      const { tier, segments } = evaluateApiKeyStrength(generateSecureApiKey());
      expect(tier).toBe('strong');
      expect(segments).toBe(API_KEY_STRENGTH_SEGMENTS);
    }
  });

  test('short keys are capped regardless of charset richness', () => {
    // 7 位就算四类字符齐全也只能是最弱档
    expect(evaluateApiKeyStrength('aA1!bB2').tier).toBe('weak');
    // 15 位封顶在第二档
    expect(evaluateApiKeyStrength('aA1!bB2@cC3#dD4').tier).toBe('fair');
    // 23 位封顶在第三档
    expect(evaluateApiKeyStrength('aA1!bB2@cC3#dD4$eE5%fG').tier).toBe('good');
  });

  test('repeated and sequential runs are discounted', () => {
    const repeated = evaluateApiKeyStrength('a'.repeat(48));
    expect(repeated.tier).toBe('weak');

    const sequential = evaluateApiKeyStrength('abcdefghijklmnopqrstuvwxyz0123456789');
    const shuffled = evaluateApiKeyStrength('qzmXe4Rk9BtLw7Ncy2VsJp5Ghd8FaU3Zmr6Q');
    expect(sequential.bits).toBeLessThan(shuffled.bits);
  });

  test('periodic keys score like a single period', () => {
    // 32 位却只有 8 位的猜测成本
    expect(evaluateApiKeyStrength('deadbeefdeadbeefdeadbeefdeadbeef').tier).toBe('fair');
    expect(evaluateApiKeyStrength('abababababababababababababab').tier).toBe('weak');
    // 尾部不完整的周期同样识别
    const periodic = evaluateApiKeyStrength('myproxy2024myproxy2024myproxy');
    const aperiodic = evaluateApiKeyStrength('myproxy2024ZtRv4Ns8Lc3Bd7hQwK');
    expect(periodic.bits).toBeLessThan(aperiodic.bits);
  });

  test('guessable tokens drag the score down', () => {
    const withToken = evaluateApiKeyStrength('sk-password-9fKw2mQx7ZtRv4Ns8Lc3Bd');
    const withoutToken = evaluateApiKeyStrength('sk-hRvnqtwj-9fKw2mQx7ZtRv4Ns8Lc3Bd');

    expect(withToken.bits).toBeLessThan(withoutToken.bits);
    expect(TIER_ORDER.indexOf(withToken.tier)).toBeLessThan(TIER_ORDER.indexOf(withoutToken.tier));
  });

  test('longer keys never score below their prefix', () => {
    const base = 'Xk7#mQ2vLp9$Rn4wZt6cHj8&Bd5xVy3';
    let previous = 0;

    for (let length = 1; length <= base.length; length += 1) {
      const { bits } = evaluateApiKeyStrength(base.slice(0, length));
      expect(bits).toBeGreaterThanOrEqual(previous);
      previous = bits;
    }
  });
});
