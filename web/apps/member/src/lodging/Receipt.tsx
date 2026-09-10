import { cn } from '@kapsora/ui';
import { useState, type ReactNode } from 'react';

export interface ReceiptLine {
  label: string;
  value: string;
  /** Money and codes are monospaced; words are not. */
  mono?: boolean;
  /** A line that explains the one above it, in the muted voice. */
  note?: string;
}

/**
 * The receipt: the booking as lines that fill in as the member chooses, with the amount
 * they will pay as the last, largest line and the one action under it.
 *
 * It is one component in one place — pinned to the bottom of the phone, a column on a
 * wide screen — so the member never has to look for what they will pay. Every value is a
 * server string; the receipt adds nothing up.
 */
export function Receipt({
  title,
  lines,
  total,
  action,
  children,
  className,
  pinned = false,
  arrivalKey,
}: {
  title: string;
  lines: ReceiptLine[];
  total?: { label: string; amount: string } | undefined;
  action?: ReactNode;
  children?: ReactNode;
  className?: string;
  /** Pinned: sticks to the bottom of the viewport above the tab bar. */
  pinned?: boolean;
  /** Changes when lines were appended; the receipt's one authored moment plays then. */
  arrivalKey?: string | undefined;
}) {
  // The lines settle in only when something new arrived: the parent changes `arrivalKey`
  // when it appends (the search page, as a room is chosen); a receipt that mounts whole
  // (the booking page) passes none and animates nothing.
  const [seenKey, setSeenKey] = useState(arrivalKey);
  const [settling, setSettling] = useState(false);
  if (seenKey !== arrivalKey) {
    setSeenKey(arrivalKey);
    setSettling(arrivalKey !== undefined);
  }
  return (
    <section
      aria-label={title}
      data-testid="receipt"
      className={cn(
        'bg-surface-raised border-line rounded-lg border p-4 shadow-[0_-2px_8px_rgba(20,32,46,0.06)]',
        pinned && 'sticky bottom-[calc(3.5rem+env(safe-area-inset-bottom))] z-20',
        className,
      )}
    >
      <h2 className="text-fg-muted text-xs font-medium tracking-[0.02em] uppercase">{title}</h2>
      <dl className="mt-2 grid gap-1">
        {lines.map((line) => (
          <div
            key={line.label}
            className={cn(
              'grid grid-cols-[auto_1fr] items-baseline gap-x-3',
              settling && 'receipt-line',
            )}
          >
            <dt className="text-fg-muted text-sm">{line.label}</dt>
            <dd
              className={cn(
                'text-right text-sm',
                line.mono && 'font-mono whitespace-nowrap tabular-nums',
              )}
            >
              {line.value}
              {line.note ? <span className="text-fg-muted block text-xs">{line.note}</span> : null}
            </dd>
          </div>
        ))}
      </dl>
      {total ? (
        <div
          className={cn(
            'border-line mt-3 grid grid-cols-[auto_1fr] items-baseline gap-x-3 border-t pt-3',
            settling && 'receipt-line',
          )}
        >
          <span className="text-sm font-medium">{total.label}</span>
          <span
            className="text-right font-mono text-lg font-semibold whitespace-nowrap tabular-nums"
            data-testid="receipt-member-amount"
          >
            {total.amount}
          </span>
        </div>
      ) : null}
      {children}
      {action ? <div className="mt-3 grid gap-2">{action}</div> : null}
    </section>
  );
}
