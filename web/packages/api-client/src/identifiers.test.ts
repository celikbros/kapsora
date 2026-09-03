import { describe, expect, it } from 'vitest';
import {
  isValidTCKN,
  isValidVKN,
  maskIdentifier,
  randomTCKN,
  randomVKN,
  validateIdentifier,
} from './identifiers';

function seeded(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

describe('VKN', () => {
  it('accepts generated numbers and rejects a corrupted check digit', () => {
    const random = seeded(7);
    for (let i = 0; i < 200; i++) {
      const vkn = randomVKN(random);
      expect(isValidVKN(vkn)).toBe(true);
      const last = Number(vkn[9]);
      const corrupted = vkn.slice(0, 9) + String((last + 1) % 10);
      expect(isValidVKN(corrupted)).toBe(false);
    }
  });

  it('agrees with the server on the classic example', () => {
    // "1234567890" is checksum-valid (every weighted term is zero); the Go tests rely on it.
    expect(isValidVKN('1234567890')).toBe(true);
    expect(isValidVKN('1234567891')).toBe(false);
    expect(isValidVKN('123 456 78 90')).toBe(true);
    expect(isValidVKN('12345')).toBe(false);
  });
});

describe('TCKN', () => {
  it('accepts generated numbers and rejects corruption', () => {
    const random = seeded(11);
    for (let i = 0; i < 200; i++) {
      const tckn = randomTCKN(random);
      expect(isValidTCKN(tckn)).toBe(true);
      const d = Number(tckn[10]);
      expect(isValidTCKN(tckn.slice(0, 10) + String((d + 1) % 10))).toBe(false);
    }
    expect(isValidTCKN('01234567890')).toBe(false);
  });
});

describe('validateIdentifier and mask', () => {
  it('maps to the same codes the form shows', () => {
    expect(validateIdentifier('VKN', '')).toBe('REQUIRED');
    expect(validateIdentifier('VKN', '1234567891')).toBe('VKN_INVALID');
    expect(validateIdentifier('TCKN', '12345678901')).toBe('TCKN_INVALID');
    expect(validateIdentifier('MERSIS', '1234')).toBe('MERSIS_INVALID');
    expect(validateIdentifier('OTHER', 'ABC-1')).toBeNull();
  });

  it('masks like the Go domain package', () => {
    expect(maskIdentifier('VKN', '1234567890')).toBe('12******90');
    expect(maskIdentifier('TCKN', '12345678901')).toBe('123******01');
    expect(maskIdentifier('MERSIS', '0123456789012345')).toBe('************2345');
    expect(maskIdentifier('OTHER', 'ABCDEF')).toBe('AB****');
  });
});
