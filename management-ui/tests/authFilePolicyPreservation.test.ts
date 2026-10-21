import { describe, expect, test } from 'bun:test';
import {
  buildCredentialPolicyPatch,
  credentialPolicyError,
  readCredentialPolicy,
} from '@/features/authFiles/credentialPolicy';

// Existing or future metadata must not be rewritten simply by opening the structured form.
const fixtures: Record<string, unknown>[] = [
  {},
  { model_aliases: null, request_scoped_errors: null },
  { model_aliases: { future: true }, request_scoped_errors: 'future-format' },
  { model_aliases: [null, 'unknown'], request_scoped_errors: [null, 'unknown'] },
  {
    model_aliases: [
      {
        name: 'upstream',
        alias: 'public',
        fork: false,
        'force-mapping': false,
        future: { retained: true },
      },
    ],
    request_scoped_errors: [
      {
        status: 429,
        match: [],
        'match-regexr': ['(?i)limit'],
        action: ' Continue ',
        future: { retained: true },
      },
    ],
  },
  {
    model_aliases: [{ name: '', alias: 'imported-invalid', fork: 'future-value' }],
    request_scoped_errors: [{ status: 0, action: 'future', match: [null] }],
  },
];

describe('structured credential policy migration safety', () => {
  for (const [index, original] of fixtures.entries()) {
    test(`does not dirty or reject untouched metadata fixture ${index}`, () => {
      const before = structuredClone(original);
      const draft = readCredentialPolicy(original);
      expect(credentialPolicyError(draft)).toBeNull();
      expect(buildCredentialPolicyPatch(original, draft)).toEqual({});
      expect(original).toEqual(before);
    });
  }
});
