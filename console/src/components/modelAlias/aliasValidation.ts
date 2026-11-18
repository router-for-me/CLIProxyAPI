const normalizeModelAliasKey = (value: string): string => value.trim().toLowerCase();

export function hasModelAliasConflict(
  aliases: string[],
  candidate: string,
  excludedAlias?: string
): boolean {
  const candidateKey = normalizeModelAliasKey(candidate);
  if (!candidateKey) return false;

  let excluded = false;
  return aliases.some((alias) => {
    if (!excluded && excludedAlias !== undefined && alias === excludedAlias) {
      excluded = true;
      return false;
    }
    return normalizeModelAliasKey(alias) === candidateKey;
  });
}
