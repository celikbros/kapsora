import type { Booking, NoShowResult } from '@kapsora/api-client';
import { formatDate, formatDateTime, formatMoney, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Spinner,
  useToast,
} from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { DocumentsPanel, useLinkedDocuments } from '../documents';
import { problemOf } from '../problems';
import { MemberName } from './MemberName';
import { useBookings, useCheckIn, useCheckOut, useProperties, useReportNoShow } from './queries';

/**
 * Gelenler / Gidenler: the day at the door.
 *
 * Arrivals are CONFIRMED bookings by arrival date; the guest is checked in by their
 * voucher token, typed or scanned, and a token that is not this booking's is refused by
 * name. Departures are CHECKED_IN bookings; check-out records what the server computes as
 * the nights slept. A guest who never came is reported as a no-show with evidence, and the
 * screen says that a second person confirms it.
 */
export function DeskPage() {
  const { t } = useTranslation();
  const properties = useProperties();
  const arrivals = useBookings('CONFIRMED');
  const departures = useBookings('CHECKED_IN');
  const names = new Map((properties.data?.items ?? []).map((p) => [p.id, p.name] as const));

  const sortByArrival = (items: Booking[]) =>
    [...items].sort((a, b) => a.checkIn.localeCompare(b.checkIn));

  return (
    <div className="grid gap-4">
      <PageHeader title={t('provider.lodging.desk.title')} />
      <section aria-labelledby="arrivals" className="grid gap-2">
        <h2 id="arrivals" className="text-base font-semibold">
          {t('provider.lodging.desk.arrivals')}
        </h2>
        {arrivals.isPending ? (
          <Spinner />
        ) : arrivals.isError ? (
          <ProblemAlert problem={problemOf(arrivals.error)} />
        ) : arrivals.data.items.length === 0 ? (
          <p className="text-fg-muted text-sm">{t('provider.lodging.desk.noArrivals')}</p>
        ) : (
          <ul className="grid gap-2" data-testid="arrival-list">
            {sortByArrival(arrivals.data.items).map((b) => (
              <ArrivalRow key={b.id} booking={b} propertyName={names.get(b.propertyId) ?? '…'} />
            ))}
          </ul>
        )}
      </section>

      <section aria-labelledby="departures" className="grid gap-2">
        <h2 id="departures" className="text-base font-semibold">
          {t('provider.lodging.desk.departures')}
        </h2>
        {departures.isPending ? (
          <Spinner />
        ) : departures.isError ? (
          <ProblemAlert problem={problemOf(departures.error)} />
        ) : departures.data.items.length === 0 ? (
          <p className="text-fg-muted text-sm">{t('provider.lodging.desk.noDepartures')}</p>
        ) : (
          <ul className="grid gap-2" data-testid="departure-list">
            {sortByArrival(departures.data.items).map((b) => (
              <DepartureRow key={b.id} booking={b} propertyName={names.get(b.propertyId) ?? '…'} />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function BookingHead({ booking, propertyName }: { booking: Booking; propertyName: string }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
      <div className="min-w-0">
        <p className="text-sm font-medium">
          <MemberName personId={booking.personId} />
        </p>
        <p className="text-fg-muted text-sm">
          {propertyName} · {formatDate(booking.checkIn)} – {formatDate(booking.checkOut)} ·{' '}
          {t('lodging.search.nights', { count: booking.nights })}
        </p>
      </div>
      <div className="flex items-center gap-2">
        <span className="font-mono text-xs">{booking.reference}</span>
        <Badge tone={booking.status === 'CHECKED_IN' ? 'success' : 'info'}>
          {t(`lodging.booking.status.${booking.status}`)}
        </Badge>
      </div>
    </div>
  );
}

function ArrivalRow({ booking, propertyName }: { booking: Booking; propertyName: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const checkIn = useCheckIn();
  const [token, setToken] = useState('');
  const [reporting, setReporting] = useState(false);

  function submit(e: FormEvent) {
    e.preventDefault();
    checkIn.mutate(
      { bookingId: booking.id, body: { token: token.trim() } },
      {
        onSuccess: () => {
          setToken('');
          toast.notify({ tone: 'success', title: t('provider.lodging.desk.checkedIn') });
        },
      },
    );
  }

  return (
    <li className="bg-surface border-border rounded-lg border p-3" data-testid="arrival-row">
      <BookingHead booking={booking} propertyName={propertyName} />
      <form onSubmit={submit} className="mt-3 flex flex-wrap items-end gap-2" noValidate>
        <FormField label={t('provider.lodging.desk.token')} className="min-w-0 flex-1">
          <Input
            name={`token-${booking.id}`}
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
          />
        </FormField>
        <Button type="submit" size="sm" loading={checkIn.isPending} disabled={token.trim() === ''}>
          {t('provider.lodging.desk.checkIn')}
        </Button>
        <Button type="button" size="sm" variant="secondary" onClick={() => setReporting((v) => !v)}>
          {t('provider.lodging.desk.noShow')}
        </Button>
      </form>
      <ProblemAlert problem={checkIn.isError ? problemOf(checkIn.error) : null} className="mt-2" />
      {reporting ? <NoShowForm booking={booking} onDone={() => setReporting(false)} /> : null}
    </li>
  );
}

function DepartureRow({ booking, propertyName }: { booking: Booking; propertyName: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const checkOut = useCheckOut();
  return (
    <li className="bg-surface border-border rounded-lg border p-3" data-testid="departure-row">
      <BookingHead booking={booking} propertyName={propertyName} />
      <p className="text-fg-muted mt-2 text-sm">
        {t('provider.lodging.desk.checkedInAt')}: {formatDateTime(booking.checkedInAt ?? null)}
      </p>
      <p className="text-fg-muted text-sm">{t('provider.lodging.desk.checkOutHint')}</p>
      <div className="mt-2">
        <Button
          size="sm"
          onClick={() =>
            checkOut.mutate(booking.id, {
              onSuccess: (result) =>
                toast.notify({
                  tone: 'success',
                  title: t('provider.lodging.desk.checkedOut', {
                    count: result.data.actualNights ?? booking.nights,
                  }),
                }),
            })
          }
          loading={checkOut.isPending}
        >
          {t('provider.lodging.desk.checkOut')}
        </Button>
      </div>
      <ProblemAlert
        problem={checkOut.isError ? problemOf(checkOut.error) : null}
        className="mt-2"
      />
    </li>
  );
}

/**
 * The no-show report: evidence first, then the command. The assessed fee is the server's
 * answer, shown after the report, and the note says a second person confirms it.
 */
function NoShowForm({ booking, onDone }: { booking: Booking; onDone: () => void }) {
  const { t } = useTranslation();
  const report = useReportNoShow();
  const documents = useLinkedDocuments('BOOKING', booking.id);
  const [result, setResult] = useState<NoShowResult | null>(null);
  const evidence = (documents.data?.items ?? []).find((d) => d.scanStatus === 'CLEAN');

  if (result) {
    return (
      <div className="bg-warning-soft mt-3 rounded-md p-3 text-sm" data-testid="no-show-result">
        <p className="font-medium">{t('provider.lodging.desk.noShowReported')}</p>
        <p className="mt-1">
          {t('provider.lodging.desk.assessedFee')}:{' '}
          <span className="font-mono tabular-nums">
            {formatMoney(result.report.assessedFeeAmount, result.report.currencyCode)}
          </span>
        </p>
        <p className="text-fg-muted mt-1">{t('provider.lodging.desk.noShowReviewNote')}</p>
      </div>
    );
  }

  return (
    <div className="border-border mt-3 grid gap-3 border-t pt-3" data-testid="no-show-form">
      <p className="text-sm">{t('provider.lodging.desk.noShowIntro')}</p>
      <DocumentsPanel
        aggregateType="BOOKING"
        aggregateId={booking.id}
        requiredTypes={['NO_SHOW_EVIDENCE']}
      />
      <ProblemAlert problem={report.isError ? problemOf(report.error) : null} />
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          disabled={!evidence}
          loading={report.isPending}
          onClick={() =>
            report.mutate(
              { bookingId: booking.id, body: { evidenceDocumentId: evidence!.id } },
              { onSuccess: (r) => setResult(r) },
            )
          }
        >
          {t('provider.lodging.desk.noShowSubmit')}
        </Button>
        <Button size="sm" variant="secondary" onClick={onDone}>
          {t('common.cancel')}
        </Button>
      </div>
      {!evidence ? (
        <p className="text-fg-muted text-xs">{t('provider.lodging.desk.noShowEvidence')}</p>
      ) : null}
    </div>
  );
}
