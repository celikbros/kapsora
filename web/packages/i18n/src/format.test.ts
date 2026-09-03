import { describe, expect, it } from 'vitest';
import { formatDate, formatDateTime, formatMoney, formatNumber } from './format';
import { fieldErrorMessage, initI18n, problemMessage } from './index';

describe('formatting', () => {
  it('formats plain dates without time zone shifts', () => {
    expect(formatDate('2026-09-03')).toBe('03.09.2026');
    expect(formatDate(null)).toBe('');
    expect(formatDate('not a date')).toBe('');
  });

  it('formats timestamps in the tenant time zone', () => {
    // 21:30 UTC is 00:30 next day in Istanbul (UTC+3).
    expect(formatDateTime('2026-09-03T21:30:00Z')).toBe('04.09.2026 00:30');
    expect(formatDateTime('2026-09-03T21:30:00Z', { timeZone: 'UTC' })).toBe('03.09.2026 21:30');
  });

  it('formats money with the ISO code, never a symbol', () => {
    expect(formatMoney(1250, 'TRY')).toBe('1.250,00 TRY');
    expect(formatMoney('99.5', 'EUR')).toBe('99,50 EUR');
    expect(formatMoney(null, 'TRY')).toBe('');
    expect(formatMoney(12, 'XXX')).toContain('XXX');
  });

  it('formats numbers with locale separators', () => {
    expect(formatNumber(1234567)).toBe('1.234.567');
    expect(formatNumber(1.5, {}, 2)).toBe('1,50');
  });
});

describe('message lookup', () => {
  const i18n = initI18n('tr');
  const t = i18n.getFixedT('tr');

  it('maps known problem codes and falls back for unknown ones', () => {
    expect(problemMessage(t, 'INVALID_CREDENTIALS')).toBe('Kullanıcı adı veya parola hatalı.');
    expect(problemMessage(t, 'SOMETHING_NEW', 'Sunucu başlığı')).toBe('Sunucu başlığı');
    expect(problemMessage(t, 'SOMETHING_NEW')).toBe(t('problems.UNKNOWN'));
  });

  it('maps field error codes with parameters', () => {
    expect(fieldErrorMessage(t, 'MIN_LENGTH', undefined, { min: 2 })).toBe('En az 2 karakter');
    expect(fieldErrorMessage(t, 'NEW_CODE', 'sunucu mesajı')).toBe('sunucu mesajı');
    expect(fieldErrorMessage(t, 'NEW_CODE')).toBe('NEW_CODE');
  });

  it('english skeleton falls back to Turkish per key', () => {
    const en = i18n.getFixedT('en');
    expect(en('auth.loginTitle')).toBe('Sign in');
    expect(en('problems.CURSOR_INVALID')).toBe('Sayfa imleci geçersiz. Listeyi baştan yükleyin.');
  });
});
