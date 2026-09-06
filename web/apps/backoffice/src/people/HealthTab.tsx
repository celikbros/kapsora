import type { Claim } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
import { Badge, ProblemAlert, Spinner, TBody, TD, TH, THead, TR, Table } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { useOrganizationName } from '../claims/names';
import { useCasesOfPerson, useClaimsOfPerson, useReadiness } from '../claims/queries';
import { claimTone } from '../claims/status';
import { problemOf } from '../problems';

/**
 * What a decided claim cost, as the server summed it. The list carries lines and not totals,
 * and this screen adds nothing up: the figure is the readiness answer's approved total, read
 * once per decided claim. An undecided claim has no total yet and says so.
 */
function TotalCell({ claim }: { claim: Claim }) {
  const { t } = useTranslation();
  const decided = claim.status === 'APPROVED' || claim.status === 'PARTIALLY_APPROVED';
  const readiness = useReadiness(claim.id, decided);
  // A rejected claim's approved total is a decided fact — nothing was approved — and a
  // cancelled one has no total at all; neither is waiting for anything.
  if (claim.status === 'REJECTED')
    return <>{formatMoney('0', claim.lines[0]?.currencyCode ?? 'TRY')}</>;
  if (claim.status === 'CANCELLED') return <>—</>;
  if (!decided) return <span className="text-fg-muted">{t('claims.lines.totalPending')}</span>;
  if (readiness.isPending) return <>…</>;
  if (!readiness.data) return <>—</>;
  return <>{formatMoney(readiness.data.approvedTotal, readiness.data.currencyCode)}</>;
}

function OrganizationCell({ organizationId }: { organizationId: string | null | undefined }) {
  const name = useOrganizationName(organizationId);
  if (!organizationId) return <>—</>;
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/**
 * A person's health as the sponsor's HR sees it: that a case exists and when, that a claim
 * exists and where it stands. There is no column here that could carry a diagnosis, a
 * description or a clinical word — not hidden, but absent, because the financial
 * projection this screen is built on never carries one.
 */
export function HealthTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const canReadCases = usePermission('health.case.read');
  const canReadClaims = usePermission('claim.read');
  // The claim and the case carry the provider's id, not its name, and reading the name
  // needs organization.read. A column that would be a dash for every row does not exist.
  const canReadOrganizations = usePermission('organization.read');
  const cases = useCasesOfPerson(personId);
  const claims = useClaimsOfPerson(personId);

  return (
    <div className="grid min-w-0 gap-6" data-testid="health-tab">
      <p className="text-fg-muted text-sm">{t('people.health.intro')}</p>

      {canReadCases ? (
        <section aria-labelledby="person-cases" className="min-w-0">
          <h2 id="person-cases" className="text-base font-semibold">
            {t('people.health.casesTitle')}
          </h2>
          {cases.isPending ? (
            <div className="text-fg-muted mt-2 flex items-center gap-2 text-sm" aria-busy="true">
              <Spinner /> {t('common.loading')}
            </div>
          ) : cases.error ? (
            <div className="mt-2">
              <ProblemAlert problem={problemOf(cases.error)} />
            </div>
          ) : (cases.data?.items.length ?? 0) === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('people.health.casesEmpty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="person-cases">
                <THead>
                  <TR>
                    <TH>{t('health.cases.columns.openedAt')}</TH>
                    <TH>{t('health.cases.columns.status')}</TH>
                    <TH>{t('health.cases.columns.type')}</TH>
                    {canReadOrganizations ? <TH>{t('claims.columns.provider')}</TH> : null}
                    <TH className="text-right">{t('health.cases.columns.encounters')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {cases.data!.items.map((c) => (
                    <TR key={c.id}>
                      <TD>{formatDate(c.openedAt)}</TD>
                      <TD>
                        <Badge tone={c.status === 'OPEN' ? 'info' : 'neutral'}>
                          {t(`health.caseStatus.${c.status}`)}
                        </Badge>
                      </TD>
                      <TD>{t(`health.caseType.${c.caseType}`)}</TD>
                      {canReadOrganizations ? (
                        <TD>
                          <OrganizationCell organizationId={c.providerOrganizationId} />
                        </TD>
                      ) : null}
                      <TD className="text-right font-mono">{c.encounters.length}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </section>
      ) : null}

      {canReadClaims ? (
        <section aria-labelledby="person-claims" className="min-w-0">
          <h2 id="person-claims" className="text-base font-semibold">
            {t('people.health.claimsTitle')}
          </h2>
          {claims.isPending ? (
            <div className="text-fg-muted mt-2 flex items-center gap-2 text-sm" aria-busy="true">
              <Spinner /> {t('common.loading')}
            </div>
          ) : claims.error ? (
            <div className="mt-2">
              <ProblemAlert problem={problemOf(claims.error)} />
            </div>
          ) : (claims.data?.items.length ?? 0) === 0 ? (
            <p className="text-fg-muted mt-2 text-sm">{t('people.health.claimsEmpty')}</p>
          ) : (
            <div className="mt-3">
              <Table data-testid="person-claims">
                <THead>
                  <TR>
                    <TH>{t('claims.columns.reference')}</TH>
                    <TH>{t('claims.columns.status')}</TH>
                    <TH>{t('claims.columns.serviceDates')}</TH>
                    {canReadOrganizations ? <TH>{t('claims.columns.provider')}</TH> : null}
                    <TH className="text-right">{t('claims.columns.total')}</TH>
                    <TH className="text-right">{t('claims.columns.lines')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {claims.data!.items.map((c) => (
                    <TR key={c.id}>
                      <TD>
                        <Link
                          to="/claims/$claimId"
                          params={{ claimId: c.id }}
                          className="font-mono underline-offset-2 hover:underline"
                        >
                          {c.reference}
                        </Link>
                      </TD>
                      <TD>
                        <Badge tone={claimTone(c.status)}>{t(`claims.status.${c.status}`)}</Badge>
                      </TD>
                      <TD>
                        {formatDate(c.serviceDateFrom)} – {formatDate(c.serviceDateTo)}
                      </TD>
                      {canReadOrganizations ? (
                        <TD>
                          <OrganizationCell organizationId={c.providerOrganizationId} />
                        </TD>
                      ) : null}
                      <TD className="text-right font-mono">
                        <TotalCell claim={c} />
                      </TD>
                      <TD className="text-right font-mono">{c.lines.length}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </section>
      ) : null}
    </div>
  );
}
