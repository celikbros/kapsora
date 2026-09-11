import { usePermission, useTenant } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  PageHeader,
  ProblemAlert,
  Spinner,
  statusTone,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { problemOf } from '../problems';
import { useOrganization } from './queries';

export function OrganizationDetailPage() {
  const { t } = useTranslation();
  const { organizationId } = useParams({ from: '/app/organizations/$organizationId' });
  const canManage = usePermission('organization.manage');
  const tenant = useTenant();
  const query = useOrganization(organizationId);
  const fmt = {
    ...(tenant?.tenant.defaultTimeZone ? { timeZone: tenant.tenant.defaultTimeZone } : {}),
  };

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error) {
    return (
      <ProblemAlert
        page
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }
  const org = query.data.data;
  const row = (label: string, value: string | undefined | null) => (
    <>
      <dt className="text-fg-muted">{label}</dt>
      <dd>{value && value !== '' ? value : t('common.none')}</dd>
    </>
  );

  return (
    <>
      <PageHeader
        title={org.displayName}
        description={org.legalName}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('organizations.title'),
                render: (label) => (
                  <Link to="/organizations" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: org.displayName },
            ]}
          />
        }
        actions={
          canManage ? (
            <Link
              to="/organizations/$organizationId/edit"
              params={{ organizationId }}
              className="bg-surface-raised border-line-strong hover:bg-surface-sunken inline-flex h-10 items-center rounded-md border px-4 text-sm font-medium"
            >
              {t('organizations.edit')}
            </Link>
          ) : null
        }
      />
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <h2 className="text-base font-semibold">{t('organizations.detailTitle')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
            {row(
              t('organizations.fields.organizationKind'),
              t(`organizations.kinds.${org.organizationKind}`),
            )}
            {row(
              t('organizations.fields.relationshipRole'),
              t(`organizations.roles.${org.relationshipRole}`),
            )}
            <dt className="text-fg-muted">{t('organizations.fields.relationshipStatus')}</dt>
            <dd>
              <Badge tone={statusTone(org.relationshipStatus)}>
                {t(`organizations.statuses.${org.relationshipStatus}`)}
              </Badge>
            </dd>
            <dt className="text-fg-muted">{t('organizations.fields.organizationStatus')}</dt>
            <dd>
              <Badge tone={statusTone(org.organizationStatus)}>
                {t(`organizations.statuses.${org.organizationStatus}`)}
              </Badge>
            </dd>
            {row(t('organizations.fields.countryCode'), org.countryCode)}
            {row(t('organizations.fields.tenantCode'), org.tenantCode)}
            {row(t('organizations.fields.validFrom'), formatDate(org.validFrom, fmt))}
            {row(t('organizations.fields.validTo'), formatDate(org.validTo, fmt))}
            {row(t('organizations.fields.rowVersion'), String(org.rowVersion))}
            <dt className="text-fg-muted">{t('organizations.fields.organizationId')}</dt>
            <dd className="font-mono text-xs">{org.organizationId}</dd>
          </dl>
        </Card>
        <Card>
          <h2 className="text-base font-semibold">{t('organizations.fields.identifiers')}</h2>
          <ul className="mt-3 grid gap-2 text-sm">
            {org.identifiers.map((id, i) => (
              <li key={`${id.type}-${i}`} className="flex items-center gap-2">
                <Badge>{t(`organizations.identifierTypes.${id.type}`)}</Badge>
                <code className="font-mono">{id.maskedValue}</code>
                {id.primary ? <Badge tone="info">{t('organizations.fields.primary')}</Badge> : null}
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </>
  );
}
