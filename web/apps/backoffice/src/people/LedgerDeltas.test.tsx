// @vitest-environment jsdom
import type { LedgerEntry } from '@kapsora/api-client';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { LedgerDeltas } from './LedgerDeltas';

vi.mock('@kapsora/i18n', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
afterEach(cleanup);

function entry(deltas: Partial<LedgerEntry>): LedgerEntry {
  return {
    deltaTotal: '0.000000',
    deltaAvailable: '0.000000',
    deltaReserved: '0.000000',
    deltaConsumed: '0.000000',
    deltaExpired: '0.000000',
    ...deltas,
  } as LedgerEntry;
}

describe('ledger balance changes', () => {
  it.each([
    ['grant', { deltaTotal: '20.000000', deltaAvailable: '20.000000' }, ['total', 'available']],
    [
      'reserve',
      { deltaAvailable: '-3.000000', deltaReserved: '3.000000' },
      ['available', 'reserved'],
    ],
    [
      'release',
      { deltaAvailable: '3.000000', deltaReserved: '-3.000000' },
      ['available', 'reserved'],
    ],
    [
      'reverse',
      { deltaTotal: '-2.000000', deltaConsumed: '-2.000000', deltaAvailable: '2.000000' },
      ['total', 'available', 'consumed'],
    ],
  ])('shows the nonzero buckets for %s', (_movement, deltas, labels) => {
    render(<LedgerDeltas entry={entry(deltas)} />);
    for (const label of labels) {
      expect(screen.getByText(`entitlements.columns.${label}:`)).toBeTruthy();
    }
    expect(screen.queryByText('0')).toBeNull();
  });

  it('shows zero only when every bucket is unchanged', () => {
    render(<LedgerDeltas entry={entry({})} />);
    expect(screen.getByText('0')).toBeTruthy();
  });
});
