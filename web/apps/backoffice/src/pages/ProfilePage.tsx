import { tenantColor, useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Badge, Card, PageHeader, statusTone } from '@kapsora/ui';

/** "Profilim ve Yetkilerim" (v1.2 section 46 item 4), straight from /me. */
export function ProfilePage() {
  const { t } = useTranslation();
  const me = useSession((s) => s.me);
  const active = useSession((s) => s.activeTenant);
  if (!me) return null;

  return (
    <>
      <PageHeader title={t('profile.title')} />
      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('profile.actor')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
            <dt className="text-fg-muted">{t('profile.actor')}</dt>
            <dd>{me.displayName}</dd>
            <dt className="text-fg-muted">{t('profile.email')}</dt>
            <dd>{me.email ?? t('common.none')}</dd>
            <dt className="text-fg-muted">{t('profile.actorId')}</dt>
            <dd className="font-mono text-xs">{me.actorId}</dd>
          </dl>
        </Card>
        <Card>
          <h2 className="text-base font-semibold">{t('profile.memberships')}</h2>
          <ul className="mt-3 grid gap-3">
            {me.tenants.map((ctx) => {
              const color = tenantColor(ctx.tenant.code);
              const isActive = active?.tenant.id === ctx.tenant.id;
              return (
                <li key={ctx.tenant.id} className="border-line rounded-md border p-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      aria-hidden="true"
                      className="inline-block size-2.5 rounded-full"
                      style={{ background: color.accent }}
                    />
                    <span className="font-medium">{ctx.tenant.displayName}</span>
                    <span className="text-fg-muted text-xs">{ctx.tenant.code}</span>
                    <Badge tone={statusTone(ctx.tenant.status)}>{ctx.tenant.status}</Badge>
                    {isActive ? <Badge tone="info">{t('profile.active')}</Badge> : null}
                  </div>
                  <h3 className="text-fg-muted mt-3 text-xs font-semibold uppercase">
                    {t('profile.permissions')}
                  </h3>
                  <ul className="mt-1 flex flex-wrap gap-1">
                    {ctx.permissions.map((p) => (
                      <li key={p}>
                        <code className="bg-surface-sunken rounded px-1.5 py-0.5 text-xs">{p}</code>
                      </li>
                    ))}
                  </ul>
                  <h3 className="text-fg-muted mt-3 text-xs font-semibold uppercase">
                    {t('profile.scopes')}
                  </h3>
                  {ctx.scopes && ctx.scopes.length > 0 ? (
                    <ul className="mt-1 flex flex-wrap gap-1">
                      {ctx.scopes.map((s, i) => (
                        <li key={`${s.type}-${i}`}>
                          <code className="bg-surface-sunken rounded px-1.5 py-0.5 text-xs">
                            {s.type}
                            {s.id ? `:${s.id}` : ''}
                          </code>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p className="text-fg-muted mt-1 text-xs">{t('profile.noScopes')}</p>
                  )}
                </li>
              );
            })}
          </ul>
        </Card>
      </div>
    </>
  );
}
