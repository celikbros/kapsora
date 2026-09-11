import { tenantColor, useSession, useSessionStore } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { AppShell, Badge, Button, DropdownMenu, cn } from '@kapsora/ui';
import { Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router';
import { useState } from 'react';
import { NAV_ENTRIES } from '../nav';
import { currentTheme, toggleTheme } from '../theme';

/** Header + sidebar shell for every signed-in screen. */
/** Drawn icons for the theme toggle: one stroke weight, no glyph standing in for them. */
function SunIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <circle cx="10" cy="10" r="3.5" stroke="currentColor" strokeWidth="1.75" />
      <path
        d="M10 2v2M10 16v2M2 10h2M16 10h2M4.3 4.3l1.4 1.4M14.3 14.3l1.4 1.4M4.3 15.7l1.4-1.4M14.3 5.7l1.4-1.4"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
      />
    </svg>
  );
}
function MoonIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        d="M15.5 12.5A6.5 6.5 0 0 1 7.5 4.5a6.5 6.5 0 1 0 8 8Z"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function AppLayout() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const store = useSessionStore();
  const me = useSession((s) => s.me);
  const active = useSession((s) => s.activeTenant);
  const [theme, setTheme] = useState(() => currentTheme());
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  const color = active ? tenantColor(active.tenant.code) : null;
  const multiTenant = (me?.tenants.length ?? 0) > 1;

  async function logout() {
    try {
      await store.logout();
    } finally {
      await navigate({ to: '/auth/logout' });
    }
  }

  const header = (
    <div className="flex h-14 items-center gap-2 px-3 sm:gap-4 sm:px-4 md:px-6">
      <Link to="/" className="shrink-0 text-base font-semibold tracking-tight sm:text-lg">
        {t('app.name')}
      </Link>
      {active && color ? (
        <Badge
          style={{
            background: color.background,
            color: color.foreground,
            borderColor: color.accent,
          }}
          title={t('header.tenantBadge', {
            name: active.tenant.displayName,
            code: active.tenant.code,
          })}
          className="max-w-[9.5rem] shrink-0 truncate sm:max-w-64"
        >
          <span
            aria-hidden="true"
            className="mr-1.5 inline-block size-2 rounded-full"
            style={{ background: color.accent }}
          />
          {active.tenant.displayName}
          {/* The code is dropped below sm on purpose, not clipped. */}
          <span className="hidden sm:inline"> · {active.tenant.code}</span>
        </Badge>
      ) : null}
      <div className="ml-auto flex min-w-0 shrink-0 items-center gap-2">
        <Button
          variant="ghost"
          size="sm"
          className="hidden sm:inline-flex"
          onClick={() => setTheme(toggleTheme())}
          aria-label={`${t('header.theme')}: ${theme === 'dark' ? t('header.themeDark') : t('header.themeLight')}`}
        >
          {theme === 'dark' ? <MoonIcon /> : <SunIcon />}
        </Button>
        <DropdownMenu
          trigger={
            <Button variant="secondary" size="sm" aria-label={t('header.userMenu')}>
              <span className="hidden sm:inline">{me?.displayName ?? '…'}</span>
              <span className="sm:hidden">{t('header.account')}</span>
            </Button>
          }
          heading={me?.email ?? me?.displayName}
          items={[
            {
              key: 'profile',
              label: t('nav.profile'),
              onSelect: () => void navigate({ to: '/profile' }),
            },
            ...(multiTenant
              ? [
                  {
                    key: 'tenant',
                    label: t('auth.switchTenant'),
                    onSelect: () =>
                      void navigate({ to: '/auth/tenant', search: { returnTo: pathname } }),
                  },
                ]
              : []),
            { key: 'logout', label: t('auth.logout'), onSelect: () => void logout(), danger: true },
          ]}
        />
      </div>
    </div>
  );

  const nav = (
    <nav aria-label={t('nav.mainMenu')} className="p-3">
      <ul className="grid gap-0.5">
        {NAV_ENTRIES.filter((e) => !e.permission || active?.permissions.includes(e.permission)).map(
          (entry) => {
            const current =
              entry.path === '/'
                ? pathname === '/'
                : pathname.startsWith(entry.match ?? entry.path);
            return (
              <li key={entry.key}>
                <Link
                  to={entry.path}
                  aria-current={current ? 'page' : undefined}
                  className={cn(
                    'block rounded-md px-3 py-2 text-sm',
                    current
                      ? 'bg-primary-soft text-primary-strong font-medium'
                      : 'text-fg hover:bg-surface-raised',
                    !entry.implemented && 'text-fg-muted',
                  )}
                >
                  {t(entry.labelKey)}
                  {!entry.implemented ? <span className="ml-1 text-xs opacity-70">·</span> : null}
                </Link>
              </li>
            );
          },
        )}
      </ul>
    </nav>
  );

  return (
    <AppShell
      header={header}
      nav={nav}
      skipLinkLabel={t('app.skipToContent')}
      menuLabel={t('app.menu')}
      {...(color ? { accent: color.accent } : {})}
    >
      {/* Signing out clears the tenant a frame before the route changes. Every screen in
          here is tenant-scoped and would throw on that frame, so hold the outlet back. */}
      {active ? <Outlet /> : null}
    </AppShell>
  );
}
