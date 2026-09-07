import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  FormField,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  useMinWidth,
} from '@kapsora/ui';
import { useState } from 'react';

import { usePersonName } from '../claims/names';
import { problemOf } from '../problems';
import { LodgingNav } from './LodgingNav';
import { usePropertyNames } from './names';
import { useProperties, useWaitlist } from './queries';
import { waitlistTone } from './status';

function PersonCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/** The queue per property, in the order the sweep will serve it. */
export function WaitlistPage() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const [propertyId, setPropertyId] = useState('');
  const properties = useProperties();
  const names = usePropertyNames();
  const entries = useWaitlist(propertyId ? { propertyId } : {});

  const ordered = [...(entries.data ?? [])].sort(
    (a, b) => b.priority - a.priority || a.createdAt.localeCompare(b.createdAt),
  );

  return (
    <div className="grid gap-4">
      <LodgingNav />
      <PageHeader title={t('lodging.office.waitlist.title')} />
      <p className="text-fg-muted text-sm">{t('lodging.office.waitlist.intro')}</p>
      <FormField label={t('lodging.office.waitlist.property')}>
        <Select
          name="property"
          value={propertyId}
          onChange={(e) => setPropertyId(e.target.value)}
          options={(properties.data?.items ?? []).map((p) => ({ value: p.id, label: p.name }))}
          placeholder={t('lodging.office.waitlist.allProperties')}
          className="w-64"
        />
      </FormField>
      {entries.isPending ? (
        <Spinner />
      ) : entries.isError ? (
        <ProblemAlert problem={problemOf(entries.error)} />
      ) : ordered.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('lodging.office.waitlist.empty')}</p>
      ) : !wide ? (
        <ol className="grid gap-2" data-testid="waitlist-table">
          {ordered.map((e, i) => (
            <li
              key={e.id}
              className="bg-surface border-border rounded-lg border p-3"
              data-testid="waitlist-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <span className="text-sm font-medium">
                  <span className="font-mono tabular-nums">{i + 1}</span> ·{' '}
                  <PersonCell personId={e.personId} />
                </span>
                <Badge tone={waitlistTone(e.status)}>
                  {t(`lodging.waitlist.status.${e.status}`)}
                </Badge>
              </div>
              <p className="text-fg-muted mt-1 text-sm">
                {names.get(e.propertyId) ?? '…'} · {formatDate(e.checkIn)} –{' '}
                {formatDate(e.checkOut)}
              </p>
              <p className="text-fg-muted mt-1 text-sm">
                {t('lodging.office.waitlist.columns.guests')} {e.adults} + {e.children} ·{' '}
                {t('lodging.office.waitlist.columns.priority')} {e.priority}
                {e.offerExpiresAt
                  ? ` · ${t('lodging.office.waitlist.columns.offerExpires')} ${formatDateTime(e.offerExpiresAt)}`
                  : ''}
              </p>
            </li>
          ))}
        </ol>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="waitlist-table">
            <THead>
              <TR>
                <TH className="text-right">{t('lodging.office.waitlist.columns.position')}</TH>
                <TH>{t('lodging.office.waitlist.columns.person')}</TH>
                <TH>{t('lodging.office.waitlist.property')}</TH>
                <TH>{t('lodging.office.waitlist.columns.dates')}</TH>
                <TH>{t('lodging.office.waitlist.columns.guests')}</TH>
                <TH className="text-right">{t('lodging.office.waitlist.columns.priority')}</TH>
                <TH>{t('lodging.office.waitlist.columns.status')}</TH>
                <TH>{t('lodging.office.waitlist.columns.offerExpires')}</TH>
              </TR>
            </THead>
            <TBody>
              {ordered.map((e, i) => (
                <TR key={e.id} data-testid="waitlist-row">
                  <TD className="text-right font-mono tabular-nums">{i + 1}</TD>
                  <TD>
                    <PersonCell personId={e.personId} />
                  </TD>
                  <TD>{names.get(e.propertyId) ?? '…'}</TD>
                  <TD className="whitespace-nowrap">
                    {formatDate(e.checkIn)} – {formatDate(e.checkOut)}
                  </TD>
                  <TD>
                    {e.adults} + {e.children}
                  </TD>
                  <TD className="text-right font-mono tabular-nums">{e.priority}</TD>
                  <TD>
                    <Badge tone={waitlistTone(e.status)}>
                      {t(`lodging.waitlist.status.${e.status}`)}
                    </Badge>
                  </TD>
                  <TD>{e.offerExpiresAt ? formatDateTime(e.offerExpiresAt) : '—'}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}
