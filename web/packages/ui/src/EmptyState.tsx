import type { ReactNode } from 'react';

export interface EmptyStateProps {
  title: string;
  description?: string;
  action?: ReactNode;
}

/** Placeholder for empty lists and unimplemented screens. */
export function EmptyState({ title, description, action }: EmptyStateProps) {
  return (
    <div className="border-line-strong text-fg-muted flex flex-col items-center gap-2 rounded-lg border border-dashed p-10 text-center">
      <p className="text-fg text-base font-medium">{title}</p>
      {description ? <p className="max-w-prose text-sm">{description}</p> : null}
      {action ? <div className="mt-2">{action}</div> : null}
    </div>
  );
}
