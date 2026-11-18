/** Duplicate drafts never publish a partially overwritten map. Empty keys stay local. */
export function serializeLiteralDraft(rows: { key: string; value: string }[]) {
  const keys = rows.filter((row) => row.key !== '').map((row) => row.key);
  if (new Set(keys).size !== keys.length) return null;
  return Object.fromEntries(
    rows.filter((row) => row.key !== '').map(({ key, value }) => [key, value])
  );
}
