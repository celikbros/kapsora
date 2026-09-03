/**
 * Turkish registry identifier checks, mirrored from the Go domain package so forms can
 * give instant feedback. The server stays the authority (WP-I1-05 section 4.5).
 */

/** Removes spaces and dashes so pasted values validate. */
export function normalizeDigits(value: string): string {
  return value.replace(/[\s-]/g, '');
}

/** VKN: 10 digits with the GİB check digit algorithm. */
export function isValidVKN(value: string): boolean {
  const v = normalizeDigits(value);
  if (!/^\d{10}$/.test(v)) {
    return false;
  }
  let sum = 0;
  for (let i = 1; i <= 9; i++) {
    const digit = Number(v[i - 1]);
    const tmp = (digit + 10 - i) % 10;
    sum += tmp === 9 ? 9 : (tmp * 2 ** (10 - i)) % 9;
  }
  const check = (10 - (sum % 10)) % 10;
  return check === Number(v[9]);
}

/** TCKN: 11 digits, first digit non-zero, two check digits. */
export function isValidTCKN(value: string): boolean {
  const v = normalizeDigits(value);
  if (!/^[1-9]\d{10}$/.test(v)) {
    return false;
  }
  const d = [...v].map(Number) as number[];
  const odd = d[0]! + d[2]! + d[4]! + d[6]! + d[8]!;
  const even = d[1]! + d[3]! + d[5]! + d[7]!;
  const tenth = (odd * 7 - even) % 10;
  if ((tenth + 10) % 10 !== d[9]) {
    return false;
  }
  const first10 = d.slice(0, 10).reduce((a, b) => a + b, 0);
  return first10 % 10 === d[10];
}

/** Types the contract accepts. */
export type IdentifierType = 'VKN' | 'TCKN' | 'MERSIS' | 'PROVIDER_REGISTRY' | 'OTHER';

/** Client-side validation for one identifier; returns an error code or null. */
export function validateIdentifier(type: IdentifierType, value: string): string | null {
  const v = value.trim();
  if (v === '') {
    return 'REQUIRED';
  }
  switch (type) {
    case 'VKN':
      return isValidVKN(v) ? null : 'VKN_INVALID';
    case 'TCKN':
      return isValidTCKN(v) ? null : 'TCKN_INVALID';
    case 'MERSIS':
      return /^\d{16}$/.test(normalizeDigits(v)) ? null : 'MERSIS_INVALID';
    default:
      return v.length <= 80 ? null : 'TOO_LONG';
  }
}

/** Masks like the server does so mocks and UI previews agree. */
export function maskIdentifier(type: IdentifierType, value: string): string {
  const v = normalizeDigits(value);
  switch (type) {
    case 'VKN':
      return v.length === 10 ? `${v.slice(0, 2)}******${v.slice(8)}` : '*'.repeat(v.length);
    case 'TCKN':
      return v.length === 11 ? `${v.slice(0, 3)}******${v.slice(9)}` : '*'.repeat(v.length);
    case 'MERSIS':
      return `${'*'.repeat(Math.max(v.length - 4, 0))}${v.slice(-4)}`;
    default:
      return v.length > 2 ? `${v.slice(0, 2)}${'*'.repeat(v.length - 2)}` : v;
  }
}

/** Random VKN with a valid check digit (synthetic data only). */
export function randomVKN(random: () => number = Math.random): string {
  const digits: number[] = [];
  for (let i = 0; i < 9; i++) {
    digits.push(Math.floor(random() * 10));
  }
  let sum = 0;
  for (let i = 1; i <= 9; i++) {
    const tmp = (digits[i - 1]! + 10 - i) % 10;
    sum += tmp === 9 ? 9 : (tmp * 2 ** (10 - i)) % 9;
  }
  digits.push((10 - (sum % 10)) % 10);
  return digits.join('');
}

/** Random TCKN with valid check digits (synthetic data only). */
export function randomTCKN(random: () => number = Math.random): string {
  const d: number[] = [1 + Math.floor(random() * 9)];
  for (let i = 1; i < 9; i++) {
    d.push(Math.floor(random() * 10));
  }
  const odd = d[0]! + d[2]! + d[4]! + d[6]! + d[8]!;
  const even = d[1]! + d[3]! + d[5]! + d[7]!;
  d.push((((odd * 7 - even) % 10) + 10) % 10);
  d.push(d.reduce((a, b) => a + b, 0) % 10);
  return d.join('');
}
