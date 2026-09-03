import { describe, expect, it } from 'vitest';
import { hashCode, tenantColor } from './tenant-color';

describe('tenant colour', () => {
  it('is stable for the same code regardless of case', () => {
    expect(tenantColor('DEMO_A')).toEqual(tenantColor('demo_a'));
    expect(hashCode('DEMO_A')).toBe(hashCode('DEMO_A'));
  });

  it('differs for different codes and stays within the hue range', () => {
    const a = tenantColor('DEMO_A');
    const b = tenantColor('DEMO_B');
    expect(a.hue).not.toBe(b.hue);
    for (const code of ['DEMO_A', 'DEMO_B', 'BANKA_X', 'SIGORTA_Y', 'X']) {
      const c = tenantColor(code);
      expect(c.hue).toBeGreaterThanOrEqual(0);
      expect(c.hue).toBeLessThan(360);
      expect(c.background).toContain(`${c.hue})`);
    }
  });
});
