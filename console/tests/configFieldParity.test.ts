// 配置项零遗漏守卫：三方对账
//   ① 搜索索引（searchIndex.ts，唯一的机器可读字段清单）
//   ② 值键映射（constants.ts FIELD_VALUE_KEYS ↔ VisualConfigValues 叶值键）
//   ③ 分区 JSX 里实际渲染的 <FieldAnchor fieldId="…"> 锚点（源码扫描）
// 任何一方增删字段而漏改其余两方，本套件即红。

import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, test } from 'bun:test';
import { createElement, type ComponentType } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { SectionAdvanced } from '@/features/config/components/sections/SectionAdvanced';
import { SectionConnectivity } from '@/features/config/components/sections/SectionConnectivity';
import { SectionLogging } from '@/features/config/components/sections/SectionLogging';
import { SectionNetwork } from '@/features/config/components/sections/SectionNetwork';
import { SectionPayload } from '@/features/config/components/sections/SectionPayload';
import { SectionStreaming } from '@/features/config/components/sections/SectionStreaming';
import {
  COMMON_FIELD_IDS,
  CONFIG_SECTION_IDS,
  CONFIG_TAB_IDS,
  FIELD_VALUE_KEYS,
  SECTION_VALIDATION_FIELDS,
} from '@/features/config/constants';
import {
  CONFIG_FIELD_SEARCH_INDEX,
  findConfigFieldById,
  searchConfigFields,
} from '@/features/config/searchIndex';
import { getVisualConfigValidationErrors } from '@/hooks/useVisualConfig';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';

const INDEX_FIELD_IDS = CONFIG_FIELD_SEARCH_INDEX.map((entry) => entry.fieldId);
const INDEX_FIELD_ID_SET = new Set(INDEX_FIELD_IDS);

/** VisualConfigValues 的叶值键：顶层标量 + streaming 展开为点号叶（= dirtyFields 的键域）。 */
const LEAF_VALUE_KEYS = new Set(
  Object.keys(DEFAULT_VISUAL_VALUES).flatMap((key) =>
    key === 'streaming'
      ? Object.keys(DEFAULT_VISUAL_VALUES.streaming).map((leaf) => `streaming.${leaf}`)
      : [key]
  )
);

const sorted = (values: Iterable<string>) => [...values].sort();

describe('search index integrity', () => {
  test('field ids are unique', () => {
    expect(INDEX_FIELD_ID_SET.size).toBe(INDEX_FIELD_IDS.length);
  });

  test('every entry belongs to a canonical section', () => {
    const sectionIds = new Set<string>(CONFIG_SECTION_IDS);
    for (const entry of CONFIG_FIELD_SEARCH_INDEX) {
      expect(sectionIds.has(entry.sectionId)).toBe(true);
    }
  });

  test('resolves the routing strategy deep-link target', () => {
    expect(findConfigFieldById('routingStrategy')).toMatchObject({
      fieldId: 'routingStrategy',
      sectionId: 'network',
    });
    expect(findConfigFieldById('unknown')).toBeUndefined();
  });

  test('label / qualifier / hint keys resolve to strings in en.json', async () => {
    const json = (await Bun.file('src/i18n/locales/en.json').json()) as Record<string, unknown>;
    const resolveKey = (path: string): unknown =>
      path.split('.').reduce<unknown>((node, part) => {
        if (node && typeof node === 'object') return (node as Record<string, unknown>)[part];
        return undefined;
      }, json);

    for (const entry of CONFIG_FIELD_SEARCH_INDEX) {
      expect(typeof resolveKey(entry.labelKey)).toBe('string');
      if (entry.qualifierKey) expect(typeof resolveKey(entry.qualifierKey)).toBe('string');
      if (entry.hintKey) expect(typeof resolveKey(entry.hintKey)).toBe('string');
    }
  });
});

// 搜索也匹配 hintKey 的文字：它必须是字段仍在渲染的说明（可见或在 InfoTip 里），
// 否则搜到的字段页面上找不到匹配原因；删掉的说明也不该再被搜到。
describe('search hints are explanations the page still renders', () => {
  const translations = i18n.cloneInstance({ lng: 'en' });
  const escapeText = (value: string) =>
    renderToStaticMarkup(createElement('span', null, value)).slice(6, -7);
  const sections: Record<string, ComponentType<Record<string, unknown>>> = {
    connectivity: SectionConnectivity,
    network: SectionNetwork,
    logging: SectionLogging,
    streaming: SectionStreaming,
    advanced: SectionAdvanced,
    payload: SectionPayload as ComponentType<Record<string, unknown>>,
  };
  const markupOf = (sectionId: string) =>
    renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n: translations },
        createElement(sections[sectionId], {
          values: DEFAULT_VISUAL_VALUES,
          disabled: false,
          onChange: () => {},
        })
      )
    );
  const rendered = Object.fromEntries(Object.keys(sections).map((id) => [id, markupOf(id)]));

  test('every hintKey is rendered by its section', () => {
    for (const entry of CONFIG_FIELD_SEARCH_INDEX) {
      if (!entry.hintKey) continue;
      expect({
        fieldId: entry.fieldId,
        rendered: rendered[entry.sectionId].includes(escapeText(translations.t(entry.hintKey))),
      }).toEqual({ fieldId: entry.fieldId, rendered: true });
    }
  });

  test('removed descriptions are neither rendered nor searchable', () => {
    const removed = [
      'sections.auth.auth_dir_hint',
      'sections.tls.enable_desc',
      'sections.network.routing_strategy_hint',
      'sections.network.disable_image_generation_hint',
      'sections.network.passthrough_headers_desc',
      'sections.system.debug_desc',
      'sections.system.logging_to_file_desc',
      'sections.system.usage_statistics_enabled_desc',
      'sections.streaming.keepalive_hint',
      'serverExtras.discoveryServiceType.hint',
      'sections.payload.default_rules_desc',
      'sections.payload.default_raw_rules_desc',
      'sections.payload.override_rules_desc',
      'sections.payload.override_raw_rules_desc',
      'sections.payload.filter_rules_desc',
    ].map((key) => `config_management.visual.${key}`);
    const hintKeys = new Set(CONFIG_FIELD_SEARCH_INDEX.map((entry) => entry.hintKey));
    const everything = Object.values(rendered).join('');
    for (const key of removed) {
      expect(hintKeys.has(key)).toBe(false);
      expect({ key, rendered: everything.includes(escapeText(translations.t(key))) }).toEqual({
        key,
        rendered: false,
      });
    }
    // A query matching only removed copy finds nothing (the old "Log to File" description).
    expect(searchConfigFields('save logs to files', (key) => translations.t(key))).toEqual([]);
  });
});

describe('value-key coverage (index ↔ VisualConfigValues)', () => {
  test('FIELD_VALUE_KEYS keys are exactly the index field ids', () => {
    expect(sorted(Object.keys(FIELD_VALUE_KEYS))).toEqual(sorted(INDEX_FIELD_ID_SET));
  });

  test('the union of mapped value keys is exactly the VisualConfigValues leaf keys', () => {
    const mapped = new Set(Object.values(FIELD_VALUE_KEYS).flat());
    // 双向：漏映射的表单键 / 指向不存在键的映射，都在这里现形
    expect(sorted(mapped)).toEqual(sorted(LEAF_VALUE_KEYS));
  });
});

describe('JSX anchor parity (source scan)', () => {
  test('every index entry is rendered by exactly the section JSX, and vice versa', () => {
    const componentsDir = join(import.meta.dir, '../src/features/config/components');
    const sectionsDir = join(componentsDir, 'sections');
    const scannedFiles = [
      ...readdirSync(sectionsDir)
        .filter((name) => name.endsWith('.tsx'))
        .map((name) => join(sectionsDir, name)),
      join(componentsDir, 'fields/sharedFields.tsx'),
    ];

    const renderedFieldIds = new Set<string>();
    for (const filePath of scannedFiles) {
      const source = readFileSync(filePath, 'utf8');
      for (const match of source.matchAll(/fieldId="([^"]+)"/g)) {
        renderedFieldIds.add(match[1]);
      }
    }

    const missingFromJsx = sorted(INDEX_FIELD_IDS).filter((id) => !renderedFieldIds.has(id));
    const unknownInJsx = sorted(renderedFieldIds).filter((id) => !INDEX_FIELD_ID_SET.has(id));

    // 分区 JSX 静默丢字段 → missingFromJsx 非空；新增字段没进索引 → unknownInJsx 非空
    expect(missingFromJsx).toEqual([]);
    expect(unknownInJsx).toEqual([]);
  });
});

describe('registry consistency', () => {
  test('tab id registry is common + the canonical sections', () => {
    expect([...CONFIG_TAB_IDS]).toEqual(['common', ...CONFIG_SECTION_IDS]);
  });

  test('every validation field path lives in exactly one section bucket', () => {
    const allPaths = Object.keys(getVisualConfigValidationErrors(DEFAULT_VISUAL_VALUES)).sort();
    const bucketed = Object.values(SECTION_VALIDATION_FIELDS).flat();
    expect(new Set(bucketed).size).toBe(bucketed.length); // 不允许一个字段进两个桶
    expect(sorted(bucketed)).toEqual(allPaths);
  });

  test('validation field paths are real value keys', () => {
    for (const path of Object.values(SECTION_VALIDATION_FIELDS).flat()) {
      expect(LEAF_VALUE_KEYS.has(path)).toBe(true);
    }
  });

  test('common tab fields are a subset of the index', () => {
    for (const fieldId of COMMON_FIELD_IDS) {
      expect(INDEX_FIELD_ID_SET.has(fieldId)).toBe(true);
    }
  });
});

describe('v8 YAML search paths', () => {
  test('deprecated quota switches are absent from values and search metadata', () => {
    for (const fieldId of ['quotaSwitchProject', 'quotaSwitchPreviewModel']) {
      expect(LEAF_VALUE_KEYS.has(fieldId)).toBe(false);
      expect(findConfigFieldById(fieldId)).toBeUndefined();
      expect(FIELD_VALUE_KEYS).not.toHaveProperty(fieldId);
    }
  });

  for (const [fieldId, path] of [
    ['apiKeys', 'access.api-keys'],
    ['commercialMode', 'server.commercial-mode'],
    ['requestRetry', 'routing.retry.request-retry'],
    ['routingStrategy', 'routing.strategy'],
    ['quotaAntigravityCredits', 'oauth.providers.antigravity.antigravity-credits'],
    ['wsAuth', 'oauth.providers.aistudio.ws-auth'],
    ['codexHeaderUserAgent', 'oauth.providers.codex.header-defaults.user-agent'],
    ['codexOptimizeMultiAgentV2', 'client.codex.optimize-multi-agent-v2'],
    ['codexResponseSteering', 'upstream.codex.response-steering'],
    ['claudeHeaderUserAgent', 'upstream.claude.header-defaults.user-agent'],
    ['xaiInjectXSearch', 'upstream.xai.inject-x-search'],
    ['streamingNonstreamKeepalive', 'requests.nonstream-keepalive-interval'],
    ['payloadDefaultRules', 'requests.payload.default'],
    ['pluginStoreAuth', 'plugins.store-auth'],
  ]) {
    test(`indexes ${path}`, () => {
      expect(findConfigFieldById(fieldId)?.yamlKeys?.join('.')).toBe(path);
      expect(searchConfigFields(path, () => '').map((entry) => entry.fieldId)).toContain(fieldId);
    });
  }
});
