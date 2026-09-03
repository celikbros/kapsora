/**
 * A stable colour per tenant code so the header looks different for each tenant and a
 * cross-tenant mistake is visible at a glance (v1.2 section 15.4).
 */

export interface TenantColor {
  /** Hue in degrees, 0-359. */
  hue: number;
  /** CSS colour for the badge background (works on light and dark themes). */
  background: string;
  /** CSS colour for text on that background. */
  foreground: string;
  /** Strong accent, e.g. a left border or the header stripe. */
  accent: string;
}

/** FNV-1a 32-bit; small, deterministic, no dependencies. */
export function hashCode(input: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    h ^= input.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

/** Colours for a tenant code. Same code always yields the same colours. */
export function tenantColor(code: string): TenantColor {
  const hue = hashCode(code.toUpperCase()) % 360;
  return {
    hue,
    background: `oklch(92% 0.06 ${hue})`,
    foreground: `oklch(30% 0.10 ${hue})`,
    accent: `oklch(55% 0.16 ${hue})`,
  };
}
