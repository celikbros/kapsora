import { useEffect, useState } from 'react';

/**
 * The seconds left until a server deadline, rendered from that deadline and nothing else.
 *
 * The clock the countdown reads is `expiresAt` — the server's own `holdExpiresAt` — so a
 * screen that slept, a tab that was restored, and a second device all agree on the same
 * moment; a timer started on click would not. When the moment passes, `onExpired` fires
 * once and the parent shows the expired state instead of a stale action.
 */
export function Countdown({
  expiresAt,
  onExpired,
  label,
}: {
  expiresAt: string;
  onExpired?: () => void;
  label: string;
}) {
  const deadline = Date.parse(expiresAt);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!Number.isFinite(deadline)) return undefined;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [deadline]);

  const left = Number.isFinite(deadline) ? Math.max(0, Math.floor((deadline - now) / 1000)) : 0;

  useEffect(() => {
    if (left === 0 && Number.isFinite(deadline)) onExpired?.();
    // The parent decides what "expired" means; this only tells it once per deadline.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [left === 0, deadline]);

  const minutes = Math.floor(left / 60);
  const seconds = left % 60;
  const text = `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;

  return (
    <p className="flex items-baseline justify-between gap-3">
      <span className="text-fg-muted text-sm">{label}</span>
      <time
        dateTime={expiresAt}
        role="timer"
        aria-live="off"
        data-testid="hold-countdown"
        className="font-mono text-lg tabular-nums"
      >
        {text}
      </time>
    </p>
  );
}
