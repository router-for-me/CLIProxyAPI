import { describe, expect, test } from 'bun:test';
import {
  MAX_CREDENTIAL_WEIGHT,
  parseCredentialWeightText,
  readCredentialWeight,
  validateCredentialWeightText,
} from '../src/utils/credentialWeight';

describe('credential weight validation', () => {
  test('accepts the default range and non-positive scheduling exclusions', () => {
    expect(parseCredentialWeightText('')).toBeUndefined();
    expect(parseCredentialWeightText('1')).toBe(1);
    expect(parseCredentialWeightText('0')).toBe(0);
    expect(parseCredentialWeightText('-2')).toBe(-2);
    expect(parseCredentialWeightText(String(MAX_CREDENTIAL_WEIGHT))).toBe(MAX_CREDENTIAL_WEIGHT);
  });

  test('rejects non-integers and values above the backend maximum', () => {
    expect(validateCredentialWeightText('1.5')).toBe('integer');
    expect(validateCredentialWeightText('1e3')).toBe('integer');
    expect(validateCredentialWeightText(String(MAX_CREDENTIAL_WEIGHT + 1))).toBe('max');
    expect(parseCredentialWeightText('1.5')).toBeUndefined();
  });

  test('reads only valid numeric response fields', () => {
    expect(readCredentialWeight(7)).toBe(7);
    expect(readCredentialWeight(0)).toBe(0);
    expect(readCredentialWeight(' 7 ')).toBe(7);
    expect(readCredentialWeight('7.5')).toBeUndefined();
    expect(readCredentialWeight(MAX_CREDENTIAL_WEIGHT + 1)).toBeUndefined();
  });
});
