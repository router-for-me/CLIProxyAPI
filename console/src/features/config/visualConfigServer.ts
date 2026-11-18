import { isMap, isScalar, isSeq, parseDocument } from 'yaml';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import type { VisualConfigValues, VisualConfigValidationErrors } from '@/types/visualConfig';
import { assertConfigListsUnchanged } from '@/services/api/configPatch';
import { readConfigBoolean } from './visualConfigBoolean';

// Backend: config_v8.go, trusted_proxies.go and discovery/service.go.
export const SERVER_FIELDS = [
  { key: 'githubToken', path: ['server', 'github-token'], kind: 'string' },
  { key: 'trustedProxies', path: ['server', 'trusted-proxies'], kind: 'list' },
  { key: 'discoveryEnabled', path: ['server', 'discovery', 'enabled'], kind: 'boolean' },
  { key: 'discoveryServiceName', path: ['server', 'discovery', 'service-name'], kind: 'string' },
  { key: 'discoveryServiceType', path: ['server', 'discovery', 'service-type'], kind: 'string' },
  { key: 'discoverySubtypes', path: ['server', 'discovery', 'subtypes'], kind: 'list' },
  {
    key: 'discoveryInterfacesInclude',
    path: ['server', 'discovery', 'interfaces', 'include'],
    kind: 'list',
  },
  {
    key: 'discoveryInterfacesExclude',
    path: ['server', 'discovery', 'interfaces', 'exclude'],
    kind: 'list',
  },
  { key: 'discoveryAuthRequired', path: ['server', 'discovery', 'auth-required'], kind: 'boolean' },
  {
    key: 'discoveryAdvertiseManagement',
    path: ['server', 'discovery', 'advertise-management'],
    kind: 'boolean',
  },
] as const;

type Doc = ReturnType<typeof parseDocument>;
const cleanList = (values: string[]) => values.map((value) => value.trim()).filter(Boolean);

export function readVisualServer(doc: Doc) {
  const values = {} as Pick<VisualConfigValues, (typeof SERVER_FIELDS)[number]['key']>;
  for (const { key, path, kind } of SERVER_FIELDS) {
    const raw = doc.getIn(path);
    Object.assign(values, {
      [key]:
        kind === 'list'
          ? isSeq(raw)
            ? cleanList(
                raw.items.flatMap((item) =>
                  isScalar(item) && typeof item.value === 'string' ? [item.value] : []
                )
              )
            : []
          : kind === 'boolean'
            ? readConfigBoolean(raw, DEFAULT_VISUAL_VALUES[key])
            : typeof raw === 'string'
              ? raw
              : DEFAULT_VISUAL_VALUES[key],
    });
  }
  return values;
}

function parents(doc: Doc, path: readonly string[]) {
  for (let i = 1; i < path.length; i++) {
    const parent = path.slice(0, i);
    const old = doc.getIn(parent, true);
    if (!isMap(old)) {
      const replacement = doc.createNode({});
      if (isScalar(old) || isSeq(old)) {
        replacement.comment = old.comment;
        replacement.commentBefore = old.commentBefore;
        replacement.spaceBefore = old.spaceBefore;
      }
      doc.setIn(parent, replacement);
    }
  }
}

/** Lists are atomic, guarded against concurrent edits, and use the retained draft AST. */
export function writeVisualServer(
  doc: Doc,
  values: VisualConfigValues,
  dirty: ReadonlySet<string>,
  sourceYaml: string,
  serverYaml: string,
  guard: boolean
) {
  const source = parseDocument(sourceYaml);
  const latestYaml = doc.toString();
  for (const { key, path, kind } of SERVER_FIELDS) {
    if (!dirty.has(key)) continue;
    const value = values[key];
    if (kind === 'list' && Array.isArray(value)) {
      const old = source.getIn(path, true);
      const seq = isSeq(old) ? old : source.createNode([]);
      // Match duplicate occurrences independently; never deduplicate or interpret patterns.
      const available = [...seq.items];
      seq.items = cleanList(value).map((text) => {
        const index = available.findIndex((item) => isScalar(item) && item.value === text);
        return index < 0 ? source.createNode(text) : available.splice(index, 1)[0];
      });
      parents(source, path);
      source.setIn(path, seq);
      if (guard) assertConfigListsUnchanged(serverYaml, source.toString(), latestYaml, [path]);
      parents(doc, path);
      doc.setIn(path, seq);
    } else {
      parents(doc, path);
      const old = doc.getIn(path, true);
      const desired = typeof value === 'string' ? value.trim() : value;
      if (isScalar(old)) old.value = desired;
      else doc.setIn(path, desired);
    }
  }
}

function validIPv4(raw: string): boolean {
  return (
    /^(?:0|[1-9]\d{0,2})(?:\.(?:0|[1-9]\d{0,2})){3}$/.test(raw) &&
    raw.split('.').every((part) => Number(part) <= 255)
  );
}

function validIPv6(raw: string): boolean {
  if (!raw.includes(':') || !/^[\da-fA-F:.]+$/.test(raw)) return false;
  if (raw.includes('.') && !validIPv4(raw.slice(raw.lastIndexOf(':') + 1))) return false;
  // URL's bracketed-host parser implements IPv6 syntax; the checks above reject
  // zones and its otherwise permissive embedded IPv4 spelling.
  try {
    return new URL(`http://[${raw}]/`).hostname.startsWith('[');
  } catch {
    return false;
  }
}

export function validTrustedProxy(raw: string): boolean {
  const parts = raw.trim().split('/');
  if (parts.length > 2) return false;
  const [ip, prefix] = parts;
  const ipv6 = ip.includes(':');
  if (!(ipv6 ? validIPv6(ip) : validIPv4(ip))) return false;
  return prefix === undefined || (/^\d+$/.test(prefix) && Number(prefix) <= (ipv6 ? 128 : 32));
}

export function validateVisualServer(values: VisualConfigValues): VisualConfigValidationErrors {
  const serviceType = values.discoveryServiceType.trim();
  return {
    trustedProxies: cleanList(values.trustedProxies).every(validTrustedProxy)
      ? undefined
      : 'invalid_trusted_proxies',
    discoveryServiceType:
      !values.discoveryEnabled ||
      !serviceType ||
      /^_[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,13}[a-zA-Z0-9])?\._tcp$/.test(serviceType)
        ? undefined
        : 'invalid_discovery_service_type',
  };
}
