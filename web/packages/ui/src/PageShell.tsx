import { useEffect, useState, type HTMLAttributes, type ReactNode } from 'react';
import { cn } from './cn';

export interface AppShellProps {
  /** Top bar: brand, tenant badge, user menu. */
  header: ReactNode;
  /** Side navigation: a sidebar from `md` up, a drawer below it; optional for the member PWA. */
  nav?: ReactNode;
  children: ReactNode;
  skipLinkLabel?: string;
  /** Accessible name of the drawer toggle shown below `md`. */
  menuLabel?: string;
  /** Accent stripe colour (tenant colour) shown under the header. */
  accent?: string;
}

/** A drawn icon: three strokes, one weight, no glyph standing in for it. */
function MenuIcon() {
  return (
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        d="M3 5h14M3 10h14M3 15h14"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        d="M5 5l10 10M15 5L5 15"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
    </svg>
  );
}

/**
 * Application frame: skip link, header, navigation, main landmark. From `md` up the
 * navigation is a sidebar; below it the same navigation is a drawer opened from the
 * header, so no viewport is left without a way to move. Escape and the backdrop close it.
 */
export function AppShell({
  header,
  nav,
  children,
  skipLinkLabel = 'İçeriğe geç',
  menuLabel = 'Menü',
  accent,
}: AppShellProps) {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return undefined;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open]);

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
        <div className="flex items-center">
          {nav ? (
            <button
              type="button"
              className="text-fg hover:bg-surface-sunken ml-2 inline-flex h-10 w-10 items-center justify-center rounded-md md:hidden"
              aria-label={menuLabel}
              aria-expanded={open}
              aria-controls="app-drawer"
              onClick={() => setOpen((v) => !v)}
            >
              {open ? <CloseIcon /> : <MenuIcon />}
            </button>
          ) : null}
          <div className="min-w-0 flex-1">{header}</div>
        </div>
      </header>
      <div className="flex flex-1">
        {nav ? (
          <aside className="border-line bg-surface-sunken hidden w-64 shrink-0 border-r md:block">
            {nav}
          </aside>
        ) : null}
        {nav && open ? (
          <div className="fixed inset-0 z-40 md:hidden" role="presentation">
            <button
              type="button"
              className="absolute inset-0 bg-black/40"
              aria-label={menuLabel}
              onClick={() => setOpen(false)}
            />
            <aside
              id="app-drawer"
              className="bg-surface-sunken border-line absolute inset-y-0 left-0 w-64 overflow-y-auto border-r shadow-lg"
              onClick={(e) => {
                // A choice made in the drawer closes it; the link itself still navigates.
                if ((e.target as HTMLElement).closest('a')) setOpen(false);
              }}
            >
              {nav}
            </aside>
          </div>
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

export function Card({
  children,
  className,
  ...rest
}: HTMLAttributes<HTMLElement> & { children: ReactNode }) {
  return (
    <section
      {...rest}
      className={cn(
        'bg-surface-raised border-line shadow-card rounded-lg border p-4 md:p-6 min-w-0',
        className,
      )}
    >
      {children}
    </section>
  );
}
