import { isAlias, isMap, isScalar, isSeq, parseDocument } from 'yaml';

// JavaScript's Number() does not recognize YAML's dotted infinity literals.
function sourceNumber(source: string): number {
  if (/^[-+]?\.(?:inf|Inf|INF)$/.test(source)) return source.startsWith('-') ? -Infinity : Infinity;
  return Number(source);
}

// A number's spelling may be data: Go decodes YAML scalars into []string using
// the source value (e.g. 001, +400 or 1e3). Keep it through visual previews,
// even for an alias whose anchor also has a non-string consumer elsewhere.
export function parseConfigDocument(yaml: string) {
  return parseDocument(yaml, {
    customTags: (tags) =>
      tags.map((tag) => {
        if (
          typeof tag === 'string' ||
          !tag.stringify ||
          !['tag:yaml.org,2002:int', 'tag:yaml.org,2002:float'].includes(tag.tag)
        )
          return tag;
        const stringify = tag.stringify;
        return {
          ...tag,
          stringify(node, ctx, onComment, onChompKeep) {
            if (
              node.type === 'PLAIN' &&
              node.source &&
              Object.is(sourceNumber(node.source), node.value)
            )
              return node.source;
            return stringify(node, ctx, onComment, onChompKeep);
          },
        };
      }),
  });
}

export const oauthRulesPath = ['oauth', 'request-scoped-errors'];

/** Project just the typed []string field, never its anchor definition or unknown siblings. */
export function oauthMatchTerms(
  doc: ReturnType<typeof parseDocument>,
  path: readonly (string | number)[],
  values: unknown[]
) {
  const resolve = (node: unknown): unknown => (isAlias(node) ? node.resolve(doc) : node);
  let node: unknown = resolve(doc.contents);
  for (const key of path) {
    node = resolve(isMap(node) || isSeq(node) ? node.get(key, true) : undefined);
  }
  if (!isSeq(node)) return values;
  return node.items.map((item, i) => {
    const term = resolve(item);
    return isScalar(term) && term.value !== null ? (term.source ?? values[i]) : values[i];
  });
}
