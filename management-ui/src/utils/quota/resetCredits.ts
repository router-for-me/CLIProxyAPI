export interface CodexResetCredit {
  id: string;
  status: string;
  grantedAt: string;
  expiresAt: string;
}

export interface CodexResetCreditsSummary {
  availableCount: number | null;
  applicableAvailableCount: number | null;
  credits: CodexResetCredit[];
  invalidPayload: boolean;
}

const asRecord = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const normalizeStringValue = (value: unknown): string | null => {
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : null;
  }
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value.toString();
  }
  return null;
};

const normalizeNumberValue = (value: unknown): number | null => {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) return null;
    const parsed = Number(trimmed);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
};

const normalizeCredit = (value: unknown): CodexResetCredit | null => {
  const record = asRecord(value);
  if (!record) return null;
  if (normalizeStringValue(record.reset_type ?? record.resetType) !== 'codex_rate_limits') {
    return null;
  }
  if (normalizeStringValue(record.status) !== 'available') {
    return null;
  }

  const expiresAt = normalizeStringValue(record.expires_at ?? record.expiresAt);
  if (!expiresAt) return null;

  return {
    id: normalizeStringValue(record.id) ?? '',
    status: normalizeStringValue(record.status) ?? '',
    grantedAt: normalizeStringValue(record.granted_at ?? record.grantedAt) ?? '',
    expiresAt,
  };
};

export const normalizeCodexResetCreditsPayload = (payload: unknown): CodexResetCreditsSummary => {
  let parsedPayload = payload;
  if (typeof payload === 'string') {
    const trimmed = payload.trim();
    if (!trimmed) {
      return {
        availableCount: null,
        applicableAvailableCount: null,
        credits: [],
        invalidPayload: true,
      };
    }
    try {
      parsedPayload = JSON.parse(trimmed);
    } catch {
      return {
        availableCount: null,
        applicableAvailableCount: null,
        credits: [],
        invalidPayload: true,
      };
    }
  }

  const record = asRecord(parsedPayload);
  if (!record) {
    return {
      availableCount: null,
      applicableAvailableCount: null,
      credits: [],
      invalidPayload: true,
    };
  }

  const hasExpectedShape =
    'credits' in record ||
    'available_count' in record ||
    'availableCount' in record ||
    'applicable_available_count' in record ||
    'applicableAvailableCount' in record;
  const credits = Array.isArray(record.credits)
    ? record.credits
        .map((item) => normalizeCredit(item))
        .filter((item): item is CodexResetCredit => Boolean(item))
    : [];

  return {
    availableCount: normalizeNumberValue(record.available_count ?? record.availableCount),
    applicableAvailableCount: normalizeNumberValue(
      record.applicable_available_count ?? record.applicableAvailableCount
    ),
    credits,
    invalidPayload: !hasExpectedShape,
  };
};
