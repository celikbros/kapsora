import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Badge, Breadcrumb, Button, PageHeader, ProblemAlert, Spinner, Tabs } from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { EligibilityTab } from './EligibilityTab';
import { EnrollmentsTab } from './EnrollmentsTab';
import { EntitlementsTab } from './EntitlementsTab';
import { FamilyTab } from './FamilyTab';
import { IdentityTab } from './IdentityTab';
import { MembershipsTab } from './MembershipsTab';
import { AccessLogTab } from './AccessLogTab';
import { HealthTab } from './HealthTab';
import { LodgingTab } from './LodgingTab';
import { ReimbursementsTab } from './ReimbursementsTab';
import { usePerson } from './queries';

/** One member with everything hanging off them, one tab per concern. */
export function PersonDetailPage() {
  const { t } = useTranslation();
  const { personId } = useParams({ from: '/app/people/$personId' });
  const query = usePerson(personId);
  const [tab, setTab] = useState('identity');
  const canReadEntitlements = usePermission('entitlement.read');
  const canCheckEligibility = usePermission('eligibility.check');
  const canReadPrograms = usePermission('program.read');
  const canReadCases = usePermission('health.case.read');
  const canReadClaims = usePermission('claim.read');
  const canReadAudit = usePermission('audit.read');
  const canReadLodging = usePermission('accommodation.property.read');

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !query.data) {
    return (
      <ProblemAlert
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  const person = query.data.data;
  const etag = query.data.etag;

  return (
    <>
      <PageHeader
        title={person.displayName}
        description={person.maskedPrimaryIdentifier ?? undefined}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('people.title'),
                render: (label) => (
                  <Link to="/people" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: person.displayName },
            ]}
          />
        }
        actions={
          <Badge tone={person.status === 'ACTIVE' ? 'success' : 'neutral'}>
            {t(`people.statuses.${person.status}`)}
          </Badge>
        }
      />

      {person.status === 'MERGED' && person.mergedIntoId ? (
        <p
          role="status"
          className="bg-info-soft text-fg mb-4 rounded-md border border-info/40 p-3 text-sm"
        >
          {t('people.mergedInto')}{' '}
          <Link
            to="/people/$personId"
            params={{ personId: person.mergedIntoId }}
            className="underline underline-offset-2"
          >
            {person.mergedIntoId}
          </Link>
        </p>
      ) : null}

      <Tabs
        ariaLabel={t('people.detailTitle')}
        value={tab}
        onValueChange={setTab}
        tabs={[
          {
            value: 'identity',
            label: t('people.tabs.identity'),
            content: <IdentityTab person={person} etag={etag} />,
          },
          {
            value: 'family',
            label: t('people.tabs.family'),
            content: <FamilyTab personId={personId} />,
          },
          {
            value: 'memberships',
            label: t('people.tabs.memberships'),
            content: <MembershipsTab personId={personId} />,
          },
          {
            value: 'enrollments',
            label: t('people.tabs.enrollments'),
            visible: canReadPrograms,
            content: <EnrollmentsTab personId={personId} />,
          },
          {
            value: 'entitlements',
            label: t('people.tabs.entitlements'),
            visible: canReadEntitlements,
            content: <EntitlementsTab personId={personId} />,
          },
          {
            value: 'eligibility',
            label: t('people.tabs.eligibility'),
            visible: canCheckEligibility,
            content: <EligibilityTab personId={personId} />,
          },
          {
            value: 'health',
            label: t('people.tabs.health'),
            visible: canReadCases || canReadClaims,
            content: <HealthTab personId={personId} />,
          },
          {
            value: 'lodging',
            label: t('people.tabs.lodging'),
            visible: canReadLodging,
            content: <LodgingTab personId={personId} />,
          },
          {
            value: 'reimbursements',
            label: t('people.tabs.reimbursements'),
            content: <ReimbursementsTab personId={personId} />,
          },
          {
            value: 'accessLog',
            label: t('people.tabs.accessLog'),
            visible: canReadAudit,
            content: <AccessLogTab personId={personId} />,
          },
        ]}
      />
    </>
  );
}
