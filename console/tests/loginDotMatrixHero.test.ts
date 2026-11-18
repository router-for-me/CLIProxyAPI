import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { DotMatrixHero, HERO_TITLE } from '@/pages/login/DotMatrixHero';
import {
  DRIFT_SPEED,
  FLASH_RISE,
  FLASH_SETTLE,
  MAX_PITCH,
  MIN_PITCH,
  TONE_DIM,
  TONE_MID,
  WAVE_DURATION,
  WAVE_JITTER,
  applyReveal,
  createDotGrid,
  fieldAt,
  pitchForHeight,
  sampleDotTargets,
} from '@/pages/login/dotMatrixField';

const WIDTH = 720;
const HEIGHT = 900;
const FPS = 30;

const grid = createDotGrid(WIDTH, HEIGHT);
const count = grid.columns * grid.rows;
const sample = (time: number) => {
  const out = new Float32Array(count);
  sampleDotTargets(grid, time, out);
  return out;
};
const isLit = (value: number) => value > 0;

describe('login dot-matrix grid', () => {
  test('anchors to the bottom-right corner and covers the whole panel', () => {
    expect(count).toBeGreaterThan(4000);
    const lastIndex = 0; // row 0, col 0 = bottom-right cell
    expect(grid.xs[lastIndex] + grid.dotSize).toBeLessThanOrEqual(WIDTH);
    expect(grid.ys[lastIndex] + grid.dotSize).toBeLessThanOrEqual(HEIGHT);
    expect(Math.min(...grid.xs)).toBeLessThanOrEqual(0 + grid.pitch);
    expect(Math.min(...grid.ys)).toBeLessThanOrEqual(0 + grid.pitch);
  });

  test('scales pitch with panel height within readable bounds', () => {
    expect(pitchForHeight(1080)).toBe(13);
    expect(pitchForHeight(200)).toBe(MIN_PITCH);
    expect(pitchForHeight(4000)).toBe(MAX_PITCH);
  });

  test('returns an empty grid for degenerate sizes', () => {
    const empty = createDotGrid(0, HEIGHT);
    expect(empty.columns * empty.rows).toBe(0);
    expect(createDotGrid(WIDTH, -1).xs.length).toBe(0);
  });

  test('is deterministic for the same time', () => {
    expect(sample(3.2)).toEqual(sample(3.2));
  });
});

describe('login dot-matrix steady state', () => {
  test('uses only the dim and mid tones, mostly dim', () => {
    const frame = sample(5);
    const lit = Array.from(frame).filter(isLit);
    const dim = Math.fround(TONE_DIM);
    const mid = Math.fround(TONE_MID);
    expect(lit.every((value) => value === dim || value === mid)).toBe(true);
    const midShare = lit.filter((value) => value === mid).length / lit.length;
    expect(midShare).toBeGreaterThan(0.05);
    expect(midShare).toBeLessThan(0.25);
  });

  test('keeps clusters with dark voids instead of a uniform carpet', () => {
    const fraction = Array.from(sample(5)).filter(isLit).length / count;
    expect(fraction).toBeGreaterThan(0.25);
    expect(fraction).toBeLessThan(0.5);
  });

  test('keeps changing continuously like the reference video', () => {
    // 视频实测：每帧约 2.9% 的点亮/熄灭切换，1 秒后点亮状态仅约 45% 重合
    const frames = Array.from({ length: FPS }, (_, index) => sample(5 + index / FPS));
    let flips = 0;
    let litTotal = 0;
    for (let f = 1; f < frames.length; f += 1) {
      for (let i = 0; i < count; i += 1) {
        if (isLit(frames[f - 1][i]) !== isLit(frames[f][i])) flips += 1;
        if (isLit(frames[f][i])) litTotal += 1;
      }
    }
    const flipRate = flips / litTotal;
    expect(flipRate).toBeGreaterThan(0.015);
    expect(flipRate).toBeLessThan(0.06);

    let same = 0;
    let union = 0;
    const first = frames[0];
    const later = sample(6);
    for (let i = 0; i < count; i += 1) {
      if (isLit(first[i]) || isLit(later[i])) {
        union += 1;
        if (isLit(first[i]) === isLit(later[i])) same += 1;
      }
    }
    expect(same / union).toBeGreaterThan(0.3);
    expect(same / union).toBeLessThan(0.7);
  });

  test('the nebula drifts leftward over time', () => {
    const seed = 0x0b17;
    const t = 1.5;
    const shift = Math.round(DRIFT_SPEED * t);
    // col 向左递增：向左漂移意味着 t 秒后的 col + shift 应与 t=0 的 col 最相似
    const correlation = (offset: number) => {
      let sum = 0;
      for (let row = 0; row < 60; row += 1) {
        for (let col = 10; col < 60; col += 1) {
          sum += (fieldAt(col, row, 0, seed) - 0.5) * (fieldAt(col + offset, row, t, seed) - 0.5);
        }
      }
      return sum;
    };
    expect(correlation(shift)).toBeGreaterThan(correlation(0));
    expect(correlation(shift)).toBeGreaterThan(correlation(-shift));
  });
});

describe('login dot-matrix intro', () => {
  test('ripples outward from the bottom-right corner within ~1.2s', () => {
    let nearLatest = 0;
    let farEarliest = Infinity;
    for (let i = 0; i < count; i += 1) {
      const x = grid.xs[i];
      const y = grid.ys[i];
      if (x > WIDTH * 0.85 && y > HEIGHT * 0.85)
        nearLatest = Math.max(nearLatest, grid.revealAt[i]);
      if (x < WIDTH * 0.3 && y < HEIGHT * 0.15)
        farEarliest = Math.min(farEarliest, grid.revealAt[i]);
    }
    expect(nearLatest).toBeLessThan(farEarliest);
    expect(grid.introDuration).toBeLessThanOrEqual(
      WAVE_DURATION + WAVE_JITTER + FLASH_RISE + FLASH_SETTLE + 1e-6
    );
    expect(grid.introDuration).toBeLessThanOrEqual(1.2 + 1e-6);
  });

  test('each dot is hidden, flashes, then settles to its live tone', () => {
    const tone = TONE_DIM;
    const revealAt = 0.3;
    expect(applyReveal(tone, revealAt, 0.29)).toBe(0);
    expect(applyReveal(0, revealAt, 0.6)).toBe(0);
    const peak = applyReveal(tone, revealAt, revealAt + FLASH_RISE);
    expect(peak).toBeGreaterThan(tone);
    const midway = applyReveal(tone, revealAt, revealAt + FLASH_RISE + FLASH_SETTLE / 2);
    expect(midway).toBeLessThan(peak);
    expect(midway).toBeGreaterThan(tone);
    expect(applyReveal(tone, revealAt, revealAt + FLASH_RISE + FLASH_SETTLE)).toBeCloseTo(tone, 6);
  });
});

describe('DotMatrixHero', () => {
  test('renders a decorative canvas and an English-only title in every language', async () => {
    const original = i18n.language;
    try {
      for (const language of ['zh-CN', 'en', 'ru']) {
        await i18n.changeLanguage(language);
        const markup = renderToStaticMarkup(createElement(DotMatrixHero));
        expect(markup).toContain('<canvas');
        expect(markup).toContain('aria-hidden="true"');
        expect(markup).toContain('lang="en"');
        expect(markup).toContain(HERO_TITLE);
        expect(markup).not.toContain(i18n.t('splash.subtitle'));
      }
    } finally {
      await i18n.changeLanguage(original);
    }
  });
});

describe('login dot-matrix on mobile', () => {
  test('the compact banner ripples from the top-right corner down toward the form', () => {
    const banner = createDotGrid(390, 320, { origin: 'top-right' });
    const n = banner.columns * banner.rows;
    let topRightLatest = 0;
    let bottomLeftEarliest = Infinity;
    for (let i = 0; i < n; i += 1) {
      const x = banner.xs[i];
      const y = banner.ys[i];
      if (x > 390 * 0.8 && y < 320 * 0.2)
        topRightLatest = Math.max(topRightLatest, banner.revealAt[i]);
      if (x < 390 * 0.2 && y > 320 * 0.8)
        bottomLeftEarliest = Math.min(bottomLeftEarliest, banner.revealAt[i]);
    }
    expect(topRightLatest).toBeLessThan(bottomLeftEarliest);
    expect(banner.introDuration).toBeLessThanOrEqual(1.2 + 1e-6);
    // 点距随横幅高度收紧，保证小屏也有足够的点
    expect(banner.pitch).toBe(MIN_PITCH);
    expect(n).toBeGreaterThan(1000);
  });

  test('the default origin stays bottom-right for the desktop panel', () => {
    const desktop = createDotGrid(390, 320);
    const explicit = createDotGrid(390, 320, { origin: 'bottom-right' });
    expect(desktop.revealAt).toEqual(explicit.revealAt);
  });

  test('the hero picks the top-right origin on mobile widths', async () => {
    const source = await Bun.file('src/pages/login/DotMatrixHero.tsx').text();
    expect(source).toContain("const COMPACT_QUERY = '(max-width: 768px)'");
    expect(source).toContain("origin: compact ? 'top-right' : 'bottom-right'");
  });

  test('mobile shows the dots as a top banner instead of hiding them', async () => {
    const page = await Bun.file('src/pages/LoginPage.module.scss').text();
    const hero = await Bun.file('src/pages/login/DotMatrixHero.module.scss').text();
    const brandPanel = page.slice(page.indexOf('.brandPanel {'), page.indexOf('.formPanel {'));
    expect(brandPanel).not.toContain('display: none');
    expect(brandPanel).toContain('height: var(--hero-band)');
    expect(brandPanel).toContain('pointer-events: none');
    expect(page).toMatch(/--hero-band: clamp\(/);
    // 横幅向下淡出；标语在移动端让位给表单标题
    const mobileHero = hero.slice(hero.indexOf('@media (max-width: $breakpoint-mobile)'));
    expect(mobileHero).toMatch(/mask-image: linear-gradient\(\s*to bottom/);
    expect(mobileHero).toMatch(/\.title \{\s*display: none;/);
  });
});
