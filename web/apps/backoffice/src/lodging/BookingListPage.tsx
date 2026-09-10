import type { BookingStatus } from '@kapsora/api-client';
import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
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
import { Link } from '@tanstack/react-router';
import { useState } from 'react';

import { usePersonName } from '../claims/names';
import { problemOf } from '../problems';
import { LodgingNav } from './LodgingNav';
import { usePropertyNames } from './names';
import { useBookings, useProperties } from './queries';
import { BOOKING_STATUSES, bookingTone } from './status';

function PersonCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/** Rezervasyonlar: a dense list, read for hours, filtered by status and property. */
export function BookingListPage() {
  const { t } = useTranslation();
  // One structure per viewport: the table where it fits, stacked rows with the status
  // beside the reference where a scroller would hide the answer.
  const wide = useMinWidth(768);
  const [status, setStatus] = useState('');
  const [propertyId, setPropertyId] = useState('');
  const properties = useProperties();
  const names = usePropertyNames();
  const bookings = useBookings({
    ...(status ? { status: status as BookingStatus } : {}),
    ...(propertyId ? { propertyId } : {}),
  });

  return (
    <div className="grid gap-4">
      <LodgingNav />
      <PageHeader title={t('lodging.office.bookings.title')} />
      <p className="text-fg-muted text-sm">{t('lodging.office.bookings.intro')}</p>
      <div className="flex flex-wrap gap-3">
        <FormField label={t('lodging.office.bookings.filters.status')}>
          <Select
            name="status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            options={BOOKING_STATUSES.map((s) => ({
              value: s,
              label: t(`lodging.booking.status.${s}`),
            }))}
            placeholder={t('lodging.office.bookings.filters.allStatuses')}
            className="w-56"
          />
        </FormField>
        <FormField label={t('lodging.office.bookings.filters.property')}>
          <Select
            name="property"
            value={propertyId}
            onChange={(e) => setPropertyId(e.target.value)}
            options={(properties.data?.items ?? []).map((p) => ({ value: p.id, label: p.name }))}
            placeholder={t('lodging.office.bookings.filters.allProperties')}
            className="w-64"
          />
        </FormField>
      </div>
      {bookings.isPending ? (
        <Spinner />
      ) : bookings.isError ? (
        <ProblemAlert problem={problemOf(bookings.error)} />
      ) : bookings.data.items.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('lodging.office.bookings.empty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2" data-testid="booking-table">
          {bookings.data.items.map((b) => (
            <li
              key={b.id}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="booking-row"
            >
              <div className="flex items-baseline justify-between gap-3">
                <Link
                  to="/lodging/bookings/$bookingId"
                  params={{ bookingId: b.id }}
                  className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                >
                  {b.reference}
                </Link>
                <Badge tone={bookingTone(b.status)}>
                  {t(`lodging.booking.status.${b.status}`)}
                </Badge>
              </div>
              <p className="mt-1 text-sm">
                <PersonCell personId={b.personId} /> · {names.get(b.propertyId) ?? '…'}
              </p>
              <p className="text-fg-muted mt-1 flex items-baseline justify-between gap-3 text-sm">
                <span>
                  {formatDate(b.checkIn)} – {formatDate(b.checkOut)}
                </span>
                <span className="text-fg font-mono tabular-nums">
                  {formatMoney(b.quoteSnapshot.memberAmount, b.quoteSnapshot.currencyCode)}
                </span>
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table data-testid="booking-table">
            <THead>
              <TR>
                <TH>{t('lodging.office.bookings.columns.reference')}</TH>
                <TH>{t('lodging.office.bookings.columns.person')}</TH>
                <TH>{t('lodging.office.bookings.columns.property')}</TH>
                <TH>{t('lodging.office.bookings.columns.dates')}</TH>
                <TH>{t('lodging.office.bookings.columns.status')}</TH>
                <TH className="text-right">{t('lodging.office.bookings.columns.member')}</TH>
              </TR>
            </THead>
            <TBody>
              {bookings.data.items.map((b) => (
                <TR key={b.id} data-testid="booking-row">
                  <TD>
                    <Link
                      to="/lodging/bookings/$bookingId"
                      params={{ bookingId: b.id }}
                      className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                    >
                      {b.reference}
                    </Link>
                  </TD>
                  <TD>
                    <PersonCell personId={b.personId} />
                  </TD>
                  <TD>{names.get(b.propertyId) ?? '…'}</TD>
                  <TD className="whitespace-nowrap">
                    {formatDate(b.checkIn)} – {formatDate(b.checkOut)}
                  </TD>
                  <TD>
                    <Badge tone={bookingTone(b.status)}>
                      {t(`lodging.booking.status.${b.status}`)}
                    </Badge>
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(b.quoteSnapshot.memberAmount, b.quoteSnapshot.currencyCode)}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}
