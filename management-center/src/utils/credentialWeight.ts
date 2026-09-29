export const DEFAULT_CREDENTIAL_WEIGHT = 1;
export const MAX_CREDENTIAL_WEIGHT = 1_000_000;

export type CredentialWeightError = 'integer' | 'max';

export const readCredentialWeight = (value: unknown): number | undefined => {
  const normalized =
    typeof value === 'number'
      ? value
      : typeof value === 'string' && /^[+-]?\d+$/.test(value.trim())
        ? Number(value.trim())
        : undefined;
  if (
    normalized === undefined ||
    !Number.isSafeInteger(normalized) ||
    normalized > MAX_CREDENTIAL_WEIGHT
  ) {
    return undefined;
  }
  return normalized;
};

export const validateCredentialWeightText = (value: string): CredentialWeightError | null => {
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!/^[+-]?\d+$/.test(trimmed)) return 'integer';

  const parsed = Number(trimmed);
  if (!Number.isSafeInteger(parsed)) return 'integer';
  return parsed > MAX_CREDENTIAL_WEIGHT ? 'max' : null;
};

export const parseCredentialWeightText = (value: string): number | undefined => {
  if (validateCredentialWeightText(value)) return undefined;
  const trimmed = value.trim();
  return trimmed ? Number(trimmed) : undefined;
};
