import { describe, expect, it } from 'vitest';
import { claimDecimal, validClaimDecimal } from './numbers';

describe('exact claim input', () => {
  it('accepts Turkish separators and preserves precision as text', () => {
    expect(claimDecimal(' 99999999999999,123456 ')).toBe('99999999999999.123456');
    expect(validClaimDecimal('0,000001', true)).toBe(true);
    expect(validClaimDecimal('400,50')).toBe(true);
    expect(validClaimDecimal('0')).toBe(true);
  });
  it('refuses ambiguous separators, invalid scale and zero quantities', () => {
    for (const value of ['', '-1', '1e2', '1.000,50', '1,2,3', '1.0000001', '123456789012345']) {
      expect(validClaimDecimal(value)).toBe(false);
    }
    expect(validClaimDecimal('0,000000', true)).toBe(false);
  });
});
