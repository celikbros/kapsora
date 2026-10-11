import { describe, expect, it } from 'vitest';
import { parseDecimalJson } from './decimal-json';

const fields = new Set(['available', 'quantity', 'deltaTotal']);

describe('parseDecimalJson', () => {
  it('preserves large, fractional, negative and nested decimal tokens exactly', () => {
    const result = parseDecimalJson<{
      items: Array<{
        available: string;
        quantity: string;
        version: number;
        movements: Array<{ deltaTotal: string }>;
      }>;
      count: number;
    }>(
      '{"items":[{"available":9007199254740993.000001,"quantity":-0.000001,"version":7,"movements":[{"deltaTotal":-123456789012345678.123456}]}],"count":2}',
      fields,
    );
    expect(result.items[0]).toMatchObject({
      available: '9007199254740993.000001',
      quantity: '-0.000001',
      version: 7,
      movements: [{ deltaTotal: '-123456789012345678.123456' }],
    });
    expect(result.count).toBe(2);
  });

  it('leaves already quoted decimals and escaped strings unchanged', () => {
    const result = parseDecimalJson<Record<string, unknown>>(
      '{"available":"12.000001","note":"fake \\"quantity\\": 999.999999", "\\u0064eltaTotal":123.000001,"quantity":1.25e+12,"nullable":null,"other":5}',
      fields,
    );
    expect(result['available']).toBe('12.000001');
    expect(result['deltaTotal']).toBe('123.000001');
    expect(result['note']).toBe('fake "quantity": 999.999999');
    expect(result['quantity']).toBe('1.25e+12');
    expect(result['nullable']).toBeNull();
    expect(result['other']).toBe(5);
  });

  it('rejects malformed JSON', () => {
    expect(() => parseDecimalJson('{"available": 1,}', fields)).toThrow(SyntaxError);
  });
});
