// 配置项零遗漏守卫：三方对账
//   ① 搜索索引（searchIndex.ts，唯一的机器可读字段清单）
//   ② 值键映射（constants.ts FIELD_VALUE_KEYS ↔ VisualConfigValues 叶值键）
//   ③ 分区 JSX 里实际渲染的 <FieldAnchor fieldId="…"> 锚点（源码扫描）
// 任何一方增删字段而漏改其余两方，本套件即红。

import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, test } from 'bun:test';
import {
  COMMON_FIELD_IDS,
  CONFIG_SECTION_IDS,
  CONFIG_TAB_IDS,
  FIELD_VALUE_KEYS,
  SECTION_VALIDATION_FIELDS,
} from '@/features/config/constants';
import { CONFIG_FIELD_SEARCH_INDEX, findConfigFieldById } from '@/features/config/searchIndex';
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
  test('tab id registry is common + the seven canonical sections', () => {
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
