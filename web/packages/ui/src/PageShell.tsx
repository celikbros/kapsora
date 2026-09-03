import type { ReactNode } from 'react';
import { cn } from './cn';

export interface AppShellProps {
  /** Top bar: brand, tenant badge, user menu. */
  header: ReactNode;
  /** Side navigation (desktop) or a drawer (mobile); optional for the member PWA. */
  nav?: ReactNode;
  children: ReactNode;
  skipLinkLabel?: string;
  /** Accent stripe colour (tenant colour) shown under the header. */
  accent?: string;
}

/** Application frame: skip link, header, optional sidebar, main landmark. */
export function AppShell({
  header,
  nav,
  children,
  skipLinkLabel = 'İçeriğe geç',
  accent,
}: AppShellProps) {
  return (
    <div className="bg-surface text-fg flex min-h-dvh flex-col">
      <a
        href="#main"
        className="bg-primary text-primary-fg sr-only z-50 rounded px-3 py-2 focus:not-sr-only focus:absolute focus:top-2 focus:left-2"
      >
        {skipLinkLabel}
      </a>
      <header
        className="bg-surface-raised border-line sticky top-0 z-30 border-b"
        style={accent ? { boxShadow: `inset 0 -3px 0 ${accent}` } : undefined}
      >
        {header}
      </header>
      <div className="flex flex-1">
        {nav ? (
          <aside className="border-line bg-surface-sunken hidden w-64 shrink-0 border-r md:block">
            {nav}
          </aside>
        ) : null}
        <main id="main" tabIndex={-1} className="min-w-0 flex-1 p-4 md:p-6">
          {children}
        </main>
      </div>
    </div>
  );
}

export interface PageHeaderProps {
  title: string;
  description?: string | undefined;
  actions?: ReactNode | undefined;
  breadcrumb?: ReactNode | undefined;
}

/** Page title block with optional breadcrumb and actions. */
export function PageHeader({ title, description, actions, breadcrumb }: PageHeaderProps) {
  return (
    <div className="mb-6">
      {breadcrumb}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold">{title}</h1>
          {description ? (
            <p className="text-fg-muted mt-1 max-w-prose text-sm">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex gap-2">{actions}</div> : null}
      </div>
    </div>
  );
}

export interface BreadcrumbItem {
  label: string;
  href?: string;
  /** Provided by the app so links go through the router. */
  render?: (label: string) => ReactNode;
}

export function Breadcrumb({
  items,
  ariaLabel = 'Gezinti yolu',
}: {
  items: BreadcrumbItem[];
  ariaLabel?: string;
}) {
  return (
    <nav aria-label={ariaLabel} className="text-fg-muted mb-2 text-sm">
      <ol className="flex flex-wrap items-center gap-1">
        {items.map((item, i) => {
          const last = i === items.length - 1;
          return (
            <li key={`${item.label}-${i}`} className="flex items-center gap-1">
              {item.render ? (
                item.render(item.label)
              ) : (
                <span aria-current={last ? 'page' : undefined}>{item.label}</span>
              )}
              {!last ? <span aria-hidden="true">/</span> : null}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <section
      className={cn(
        'bg-surface-raised border-line shadow-card rounded-lg border p-4 md:p-6',
        className,
      )}
    >
      {children}
    </section>
  );
}
