import { tenantColor, useSession, useSessionStore } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { AppShell, Badge, Button, DropdownMenu, cn } from '@kapsora/ui';
import { Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router';
import { useState } from 'react';
import { NAV_ENTRIES } from '../nav';
import { currentTheme, toggleTheme } from '../theme';

/** Header + sidebar shell for every signed-in screen. */
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
    <div className="flex h-14 items-center gap-4 px-4 md:px-6">
      <Link to="/" className="text-lg font-semibold tracking-tight">
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
          className="max-w-64 truncate"
        >
          <span
            aria-hidden="true"
            className="mr-1.5 inline-block size-2 rounded-full"
            style={{ background: color.accent }}
          />
          {active.tenant.displayName} · {active.tenant.code}
        </Badge>
      ) : null}
      <div className="ml-auto flex items-center gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setTheme(toggleTheme())}
          aria-label={`${t('header.theme')}: ${theme === 'dark' ? t('header.themeDark') : t('header.themeLight')}`}
        >
          {theme === 'dark' ? '☾' : '☀'}
        </Button>
        <DropdownMenu
          trigger={
            <Button variant="secondary" size="sm" aria-label={t('header.userMenu')}>
              {me?.displayName ?? '…'}
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
            const current = entry.path === '/' ? pathname === '/' : pathname.startsWith(entry.path);
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
      {...(color ? { accent: color.accent } : {})}
    >
      <Outlet />
    </AppShell>
  );
}
