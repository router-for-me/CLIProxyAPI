import { isMap as isYamlMap, parseDocument } from 'yaml';
import { apiClient } from './client';

export interface ConfigPatchPlan {
  patch: Record<string, unknown>;
  deletions: string[][];
  /** Explicit empty maps require PUT at that map, since DELETE prunes empty ancestors. */
  emptyMaps?: string[][];
}

const isMap = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

// Null-prototype records preserve literal keys such as __proto__ and constructor.
const hasOwn = (value: object, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

const record = (): Record<string, unknown> => Object.create(null);

function parseConfig(source: string): Record<string, unknown> {
  const doc = parseDocument(source, { intAsBigInt: true });
  if (doc.errors.length || doc.warnings.length) {
    throw new Error(`Invalid configuration YAML: ${[...doc.errors, ...doc.warnings][0].message}`);
  }
  const active = new Set<object>();
  function convert(value: unknown): unknown {
    if (value === null || typeof value === 'string' || typeof value === 'boolean') return value;
    if (typeof value === 'bigint') {
      const number = Number(value);
      if (!Number.isSafeInteger(number)) throw new Error('Configuration integer is not JSON-safe');
      return number;
    }
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value !== 'object' || value === null || active.has(value)) {
      throw new Error('Configuration must contain only JSON-representable values');
    }
    active.add(value);
    try {
      if (Array.isArray(value)) return value.map(convert);
      if (value instanceof Map) {
        const result = record();
        for (const [key, child] of value) {
          if (typeof key !== 'string') throw new Error('Configuration map keys must be strings');
          result[key] = convert(child);
        }
        return result;
      }
      throw new Error('Configuration must contain only JSON-representable values');
    } finally {
      active.delete(value);
    }
  }
  const result = convert(doc.toJS({ mapAsMap: true, maxAliasCount: 100 }));
  if (!isMap(result)) throw new Error('Configuration root must be a map');
  return result;
}

function equal(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((value, index) => equal(value, b[index]));
  }
  if (isMap(a) && isMap(b)) {
    return (
      Object.keys(a).length === Object.keys(b).length &&
      Object.keys(a).every((key) => hasOwn(b, key) && equal(a[key], b[key]))
    );
  }
  return false;
}

function fieldUrl(path: string[]): string {
  // Gin splits decoded paths on '/'; encoding a slash cannot make it a literal key.
  // Dot segments are normalized by URL clients before the request reaches the server.
  if (
    !path.length ||
    path.some(
      (key) =>
        !key ||
        key === '.' ||
        key === '..' ||
        key.includes('/') ||
        key.includes('\\') ||
        Array.from(key).some((char) => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)
    )
  ) {
    throw new Error('Configuration field path cannot be safely represented');
  }
  return `/config/${path.map((key) => encodeURIComponent(key)).join('/')}`;
}

export function buildConfigPatch(beforeYaml: string, afterYaml: string): ConfigPatchPlan {
  const before = parseConfig(beforeYaml);
  const after = parseConfig(afterYaml);
  const deletions: string[][] = [];
  const emptyMaps: string[][] = [];
  function remove(value: unknown, path: string[]): void {
    if (isMap(value) && Object.keys(value).length) {
      for (const key of Object.keys(value)) remove(value[key], [...path, key]);
    } else {
      fieldUrl(path);
      deletions.push(path);
    }
  }
  function diff(old: Record<string, unknown>, next: Record<string, unknown>, path: string[]) {
    const patch = record();
    for (const key of Object.keys(old)) {
      if (!hasOwn(next, key)) remove(old[key], [...path, key]);
    }
    for (const key of Object.keys(next)) {
      if (hasOwn(old, key) && equal(old[key], next[key])) continue;
      if (hasOwn(old, key) && isMap(old[key]) && isMap(next[key])) {
        if (Object.keys(next[key]).length === 0) {
          // This is an intentional reset of the entire map, not deletion of the map.
          const target = [...path, key];
          fieldUrl(target);
          emptyMaps.push(target);
          continue;
        }
        const child = diff(old[key], next[key], [...path, key]);
        if (Object.keys(child).length) patch[key] = child;
      } else {
        patch[key] = next[key];
      }
    }
    return patch;
  }
  const patch = diff(before, after, []);
  return { patch, deletions, ...(emptyMaps.length ? { emptyMaps } : {}) };
}

export class ConfigDraftConflictError extends Error {
  constructor(readonly path: readonly string[]) {
    super(`Configuration list changed concurrently: ${path.join('.')}`);
    this.name = 'ConfigDraftConflictError';
  }
}

/** Lists have no stable backend IDs: refuse an ambiguous three-way replacement. */
export function assertConfigListsUnchanged(
  beforeYaml: string,
  draftYaml: string,
  latestYaml: string,
  paths?: readonly (readonly string[])[]
): void {
  const before = parseConfig(beforeYaml);
  const draft = parseConfig(draftYaml);
  const latest = parseConfig(latestYaml);
  const readPath = (root: unknown, path: readonly string[]): unknown =>
    path.reduce<unknown>(
      (value, key) => (isMap(value) && hasOwn(value, key) ? value[key] : undefined),
      root
    );
  const check = (old: unknown, desired: unknown, current: unknown, path: readonly string[]) => {
    if (Array.isArray(old) || Array.isArray(desired)) {
      if (!equal(current, old) && !equal(current, desired)) {
        throw new ConfigDraftConflictError(path);
      }
      return;
    }
    const keys = new Set([
      ...Object.keys(isMap(old) ? old : {}),
      ...Object.keys(isMap(desired) ? desired : {}),
    ]);
    for (const key of keys) {
      const nextPath = [...path, key];
      const oldValue = readPath(before, nextPath);
      const desiredValue = readPath(draft, nextPath);
      if (!equal(oldValue, desiredValue)) {
        check(oldValue, desiredValue, readPath(latest, nextPath), nextPath);
      }
    }
  };
  if (paths) {
    for (const path of paths) {
      check(readPath(before, path), readPath(draft, path), readPath(latest, path), path);
    }
  } else {
    check(before, draft, latest, []);
  }
}

/** Replay the confirmed wire-level intent, not positional editor IDs, after a partial save. */
export function rebaseConfigDraft(
  beforeYaml: string,
  draftYaml: string,
  latestYaml: string
): string {
  const plan = buildConfigPatch(beforeYaml, draftYaml);
  // Validate readback and reject ambiguous list mutations before advancing the baseline.
  assertConfigListsUnchanged(beforeYaml, draftYaml, latestYaml);
  const doc = parseDocument(latestYaml);

  const ensureParents = (path: string[]) => {
    for (let length = 1; length < path.length; length += 1) {
      const parent = path.slice(0, length);
      if (!isYamlMap(doc.getIn(parent, true))) doc.setIn(parent, doc.createNode({}));
    }
  };
  const merge = (patch: Record<string, unknown>, parent: string[]) => {
    for (const [key, value] of Object.entries(patch)) {
      const path = [...parent, key];
      ensureParents(path);
      if (isMap(value)) {
        if (!isYamlMap(doc.getIn(path, true))) doc.setIn(path, doc.createNode({}));
        merge(value, path);
      } else {
        doc.setIn(path, doc.createNode(value));
      }
    }
  };
  merge(plan.patch, []);
  for (const path of plan.emptyMaps ?? []) {
    ensureParents(path);
    doc.setIn(path, doc.createNode({}));
  }
  for (const path of plan.deletions) {
    if (!doc.hasIn(path)) continue;
    doc.deleteIn(path);
    // Match the backend's empty-ancestor pruning, without deleting concurrent siblings.
    for (let length = path.length - 1; length > 0; length -= 1) {
      const parent = path.slice(0, length);
      const node = doc.getIn(parent, true);
      if (!isYamlMap(node) || node.items.length > 0) break;
      doc.deleteIn(parent);
    }
  }
  return doc.toString({ indent: 2, lineWidth: 120, minContentWidth: 0 });
}

export function hasConfigPatchChanges(plan: ConfigPatchPlan): boolean {
  return (
    Object.keys(plan.patch).length > 0 ||
    plan.deletions.length > 0 ||
    Boolean(plan.emptyMaps?.length)
  );
}

export async function applyConfigPatch(
  plan: ConfigPatchPlan,
  connectionRevision: number
): Promise<void> {
  const checkConnection = () => {
    if (apiClient.getConnectionRevision() !== connectionRevision) {
      throw new Error('Connection changed while saving configuration');
    }
  };
  // Validate every path before performing any mutations, including for caller-built plans.
  const urls = plan.deletions.map(fieldUrl);
  const emptyMapUrls = (plan.emptyMaps ?? []).map(fieldUrl);
  checkConnection();
  if (Object.keys(plan.patch).length) {
    try {
      await apiClient.patch('/config', plan.patch);
    } finally {
      checkConnection();
    }
  }
  for (const url of emptyMapUrls) {
    checkConnection();
    try {
      await apiClient.put(url, {});
    } finally {
      checkConnection();
    }
  }
  for (const url of urls) {
    checkConnection();
    try {
      await apiClient.delete(url);
    } catch (error: unknown) {
      if (!error || typeof error !== 'object' || !('status' in error) || error.status !== 404) {
        throw error;
      }
    } finally {
      checkConnection();
    }
  }
}
