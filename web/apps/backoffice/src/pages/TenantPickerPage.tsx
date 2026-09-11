import type { ApiError } from '@kapsora/api-client';
import { fitsApp, safeReturnTo, tenantColor, useSession, useSessionStore } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Badge, Button, Card, EmptyState, ProblemAlert, statusTone } from '@kapsora/ui';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { problemOf } from '../problems';
import { useState } from 'react';

/** Tenant selection after login (v1.2 section 46 item 2). */
export function TenantPickerPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const search = useSearch({ from: '/auth/tenant' });
  const me = useSession((s) => s.me);
  const active = useSession((s) => s.activeTenant);
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  // Only the tenants this account has backoffice work in; the rest belong to another app.
  const tenants = (me?.tenants ?? []).filter((ctx) => fitsApp(ctx, 'backoffice'));

  async function choose(tenantId: string) {
    setBusy(tenantId);
    setProblem(null);
    try {
      await store.switchTenant(tenantId);
      await navigate({ href: safeReturnTo(search.returnTo, '/') });
    } catch (err) {
      setProblem(problemOf(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-lg">
        <h1 className="text-xl font-semibold">{t('auth.tenantPickerTitle')}</h1>
        <p className="text-fg-muted mt-1 text-sm">{t('auth.tenantPickerIntro')}</p>
        <ProblemAlert problem={problem} className="mt-4" />
        {tenants.length === 0 ? (
          <div className="mt-6">
            <EmptyState title={t('auth.noTenantsTitle')} description={t('auth.noTenantsBody')} />
          </div>
        ) : (
          <ul className="mt-6 grid gap-2">
            {tenants.map((ctx) => {
              const color = tenantColor(ctx.tenant.code);
              const isActive = active?.tenant.id === ctx.tenant.id;
              return (
                <li
                  key={ctx.tenant.id}
                  className="border-line flex items-center gap-3 rounded-md border p-3"
                >
                  <span
                    aria-hidden="true"
                    className="inline-block size-2.5 shrink-0 rounded-full"
                    style={{ background: color.accent }}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium">{ctx.tenant.displayName}</p>
                    <p className="text-fg-muted text-xs">
                      {ctx.tenant.code} · {ctx.permissions.length}{' '}
                      {t('profile.permissions').toLocaleLowerCase('tr')}
                    </p>
                  </div>
                  <Badge tone={statusTone(ctx.tenant.status)}>{ctx.tenant.status}</Badge>
                  <Button
                    size="sm"
                    variant={isActive ? 'secondary' : 'primary'}
                    loading={busy === ctx.tenant.id}
                    onClick={() => void choose(ctx.tenant.id)}
                  >
                    {busy === ctx.tenant.id ? t('auth.tenantSwitching') : t('auth.tenantSelect')}
                  </Button>
                </li>
              );
            })}
          </ul>
        )}
      </Card>
    </main>
  );
}
