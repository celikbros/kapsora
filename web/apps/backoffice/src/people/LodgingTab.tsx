import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
import { Badge, ProblemAlert, Spinner, TBody, TD, TH, THead, TR, Table } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { usePropertyNames } from '../lodging/names';
import { useBookings, useWaitlist } from '../lodging/queries';
import { bookingTone, waitlistTone } from '../lodging/status';
import { problemOf } from '../problems';

/** Konaklama: the person's bookings and waiting-list entries, as they are. */
export function LodgingTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const bookings = useBookings({ personId });
  const waitlist = useWaitlist({ personId });
  const names = usePropertyNames();

  return (
    <div className="grid min-w-0 gap-6" data-testid="lodging-tab">
      <p className="text-fg-muted text-sm">{t('people.lodging.intro')}</p>
      <section aria-labelledby="person-bookings" className="min-w-0">
        <h2 id="person-bookings" className="text-base font-semibold">
          {t('people.lodging.bookingsTitle')}
        </h2>
        {bookings.isPending ? (
          <div className="text-fg-muted mt-2 flex items-center gap-2 text-sm" aria-busy="true">
            <Spinner /> {t('common.loading')}
          </div>
        ) : bookings.isError ? (
          <ProblemAlert problem={problemOf(bookings.error)} className="mt-2" />
        ) : bookings.data.items.length === 0 ? (
          <p className="text-fg-muted mt-2 text-sm">{t('people.lodging.bookingsEmpty')}</p>
        ) : (
          <div className="relative mt-2 overflow-x-auto">
            <Table>
              <THead>
                <TR>
                  <TH>{t('lodging.office.bookings.columns.reference')}</TH>
                  <TH>{t('lodging.office.bookings.columns.property')}</TH>
                  <TH>{t('lodging.office.bookings.columns.dates')}</TH>
                  <TH>{t('lodging.office.bookings.columns.status')}</TH>
                  <TH className="text-right">{t('lodging.office.bookings.columns.member')}</TH>
                </TR>
              </THead>
              <TBody>
                {bookings.data.items.map((b) => (
                  <TR key={b.id}>
                    <TD>
                      <Link
                        to="/lodging/bookings/$bookingId"
                        params={{ bookingId: b.id }}
                        className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                      >
                        {b.reference}
                      </Link>
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
      </section>
      <section aria-labelledby="person-waitlist" className="min-w-0">
        <h2 id="person-waitlist" className="text-base font-semibold">
          {t('people.lodging.waitlistTitle')}
        </h2>
        {waitlist.isPending ? (
          <div className="text-fg-muted mt-2 flex items-center gap-2 text-sm" aria-busy="true">
            <Spinner /> {t('common.loading')}
          </div>
        ) : waitlist.isError ? (
          <ProblemAlert problem={problemOf(waitlist.error)} className="mt-2" />
        ) : waitlist.data.length === 0 ? (
          <p className="text-fg-muted mt-2 text-sm">{t('people.lodging.waitlistEmpty')}</p>
        ) : (
          <ul className="mt-2 grid gap-1 text-sm">
            {waitlist.data.map((e) => (
              <li key={e.id} className="flex flex-wrap items-baseline gap-x-3">
                <span>{names.get(e.propertyId) ?? '…'}</span>
                <span className="text-fg-muted">
                  {formatDate(e.checkIn)} – {formatDate(e.checkOut)}
                </span>
                <Badge tone={waitlistTone(e.status)}>
                  {t(`lodging.waitlist.status.${e.status}`)}
                </Badge>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
