import type { HTMLAttributes, ReactNode } from 'react';
import { cn } from './cn';

export type BadgeTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger';

const tones: Record<BadgeTone, string> = {
  neutral: 'bg-surface-sunken text-fg-muted border-line',
  info: 'bg-info-soft text-info border-info/40',
  success: 'bg-success-soft text-success border-success/40',
  warning: 'bg-warning-soft text-warning border-warning/40',
  danger: 'bg-danger-soft text-danger border-danger/40',
};

/**
 * A status word. Everything a span accepts passes through — a `data-testid`, a `title` —
 * so a screen can name the badge it asserts on without wrapping it.
 */
export function Badge({
  tone = 'neutral',
  children,
  className,
  ...rest
}: HTMLAttributes<HTMLSpanElement> & {
  tone?: BadgeTone;
  children: ReactNode;
}) {
  return (
    <span
      {...rest}
      className={cn(
        'inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        tones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Maps relationship/organization statuses to a tone. */
export function statusTone(status: string): BadgeTone {
  switch (status) {
    case 'ACTIVE':
      return 'success';
    case 'PENDING':
    case 'PROVISIONING':
      return 'info';
    case 'SUSPENDED':
      return 'warning';
    case 'TERMINATED':
    case 'CLOSED':
    case 'INACTIVE':
      return 'danger';
    default:
      return 'neutral';
  }
}
