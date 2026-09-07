import { useTranslation } from '@kapsora/i18n';
import { Badge, Button, Card, ProblemAlert } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { problemOf } from '../problems';
import { Loading } from './Loading';
import { usePropertyNames } from './names';
import { useAcceptOffer, useBookings, useLeaveWaitlist, useWaitlist } from './queries';
import { bookingTone, money, stayDates } from './words';

/** Rezervasyonlarım: every booking as it is, live ones first; the waiting list under them. */
export function BookingsPage() {
  const { t } = useTranslation();
  const bookings = useBookings();
  const waitlist = useWaitlist();
  const names = usePropertyNames();
  const accept = useAcceptOffer();
  const leave = useLeaveWaitlist();

  const items = [...(bookings.data?.items ?? [])].sort((a, b) =>
    b.checkIn.localeCompare(a.checkIn),
  );
  const entries = (waitlist.data ?? []).filter(
    (e) => e.status === 'WAITING' || e.status === 'OFFERED',
  );

  return (
    <div className="grid gap-4 p-4">
      <h1 className="text-xl font-semibold">{t('lodging.booking.list')}</h1>
      {bookings.isPending ? (
        <Loading />
      ) : bookings.isError ? (
        <ProblemAlert problem={problemOf(bookings.error)} />
      ) : items.length === 0 ? (
        <Card>
          <p className="text-sm">{t('lodging.booking.empty')}</p>
        </Card>
      ) : (
        <ul className="grid gap-2" data-testid="booking-list">
          {items.map((b) => (
            <li key={b.id}>
              <Link
                to="/bookings/$bookingId"
                params={{ bookingId: b.id }}
                className="bg-surface border-border block rounded-lg border p-3"
                data-testid="booking-row"
              >
                <span className="flex items-baseline justify-between gap-3">
                  <span className="text-sm font-medium">{names.get(b.propertyId) ?? '…'}</span>
                  <Badge tone={bookingTone(b.status)}>
                    {t(`lodging.booking.status.${b.status}`)}
                  </Badge>
                </span>
                <span className="text-fg-muted mt-1 block text-sm">
                  {stayDates(b.checkIn, b.checkOut)}
                </span>
                <span className="mt-1 flex items-baseline justify-between gap-3 text-sm">
                  <span className="font-mono text-xs">{b.reference}</span>
                  <span className="font-mono tabular-nums">
                    {money(b.quoteSnapshot.memberAmount, b.quoteSnapshot.currencyCode)}
                  </span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}

      <section aria-labelledby="waitlist-heading" className="grid gap-2">
        <h2 id="waitlist-heading" className="text-base font-semibold">
          {t('lodging.waitlist.title')}
        </h2>
        {waitlist.isPending ? (
          <Loading />
        ) : waitlist.isError ? (
          <ProblemAlert problem={problemOf(waitlist.error)} />
        ) : entries.length === 0 ? (
          <p className="text-fg-muted text-sm">{t('lodging.waitlist.empty')}</p>
        ) : (
          <ul className="grid gap-2" data-testid="waitlist-list">
            {entries.map((e) => (
              <li key={e.id} className="bg-surface border-border rounded-lg border p-3">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="text-sm font-medium">{names.get(e.propertyId) ?? '…'}</span>
                  <Badge tone={e.status === 'OFFERED' ? 'success' : 'neutral'}>
                    {t(`lodging.waitlist.status.${e.status}`)}
                  </Badge>
                </div>
                <p className="text-fg-muted mt-1 text-sm">{stayDates(e.checkIn, e.checkOut)}</p>
                {e.status === 'OFFERED' ? (
                  <div className="mt-2 grid gap-2">
                    <p className="text-sm">{t('lodging.waitlist.offered')}</p>
                    <Button
                      size="sm"
                      onClick={() => accept.mutate(e.id)}
                      loading={accept.isPending}
                    >
                      {t('lodging.waitlist.accept')}
                    </Button>
                  </div>
                ) : null}
                <div className="mt-2">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => leave.mutate(e.id)}
                    loading={leave.isPending}
                  >
                    {t('lodging.waitlist.cancel')}
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
