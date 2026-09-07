import { useSession } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import { Badge, Card, ProblemAlert } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { problemOf } from '../problems';
import { Loading } from './Loading';
import { useBookings, useMyEntitlements, useMyPersonId } from './queries';
import { bookingTone, isLive, money, quantity, stayDates, unitWord } from './words';
import { usePropertyNames } from './names';

/** A link that reads as the page's one action; the classes are the primary button's. */
const BUTTON_LINK =
  'bg-primary text-primary-fg hover:bg-primary-hover inline-flex items-center justify-center rounded-md px-4 py-2 text-sm font-medium';

/**
 * Home: what is left, first. The member's question on opening the app is "how much do I
 * have", so the remaining figures lead — nights before anything else — then the next stay
 * if there is one, then the one action.
 */
export function HomePage() {
  const { t } = useTranslation();
  const me = useSession((s) => s.me);
  const personId = useMyPersonId();
  const entitlements = useMyEntitlements(personId);
  const bookings = useBookings();
  const names = usePropertyNames();

  const live = (bookings.data?.items ?? [])
    .filter(isLive)
    .sort((a, b) => a.checkIn.localeCompare(b.checkIn));
  const next = live.find((b) => b.status !== 'HOLD') ?? live[0];
  const hold = live.find((b) => b.status === 'HOLD');

  return (
    <div className="grid gap-4 p-4">
      <h1 className="text-xl font-semibold">{me?.displayName}</h1>

      {personId === null ? (
        <Card>
          <p className="text-sm">{t('lodging.home.unbound')}</p>
        </Card>
      ) : (
        <Card>
          <h2 className="text-base font-semibold">{t('lodging.home.remaining')}</h2>
          <p className="text-fg-muted mt-1 text-xs">{t('lodging.home.remainingHint')}</p>
          {entitlements.isPending ? (
            <Loading />
          ) : entitlements.isError ? (
            <ProblemAlert problem={problemOf(entitlements.error)} />
          ) : entitlements.data.length === 0 ? (
            <p className="mt-3 text-sm">{t('lodging.home.noEntitlements')}</p>
          ) : (
            <dl className="mt-3 grid gap-2" data-testid="remaining-list">
              {[...entitlements.data]
                .sort((a, b) =>
                  a.definition.unitType === 'NIGHT'
                    ? -1
                    : b.definition.unitType === 'NIGHT'
                      ? 1
                      : 0,
                )
                .map((account) => (
                  <div
                    key={account.id}
                    className="grid grid-cols-[auto_1fr_auto_auto] items-baseline gap-x-3 gap-y-0.5"
                    data-testid="remaining-row"
                  >
                    <dt className="text-sm">{account.definition.name}</dt>
                    <span
                      aria-hidden="true"
                      // A box the height of the first line: the leader sits on the name's baseline
                      // even when the name wraps.
                      className="border-border h-[1.1em] self-start border-b border-dotted"
                    />
                    <dd className="text-right font-mono text-lg font-semibold tabular-nums">
                      {account.definition.unitType === 'MONEY'
                        ? money(String(account.available), account.definition.currencyCode ?? 'TRY')
                        : quantity(account.available)}
                    </dd>
                    <dd className="text-fg-muted w-14 text-sm">
                      {account.definition.unitType === 'MONEY'
                        ? ''
                        : unitWord(t, account.definition.unitType)}
                    </dd>
                    <dd className="text-fg-muted col-span-4 text-xs">
                      {t('lodging.home.period')}: {formatDate(account.benefitPeriodFrom)}
                      {account.benefitPeriodTo ? ` – ${formatDate(account.benefitPeriodTo)}` : ''}
                      {account.shared ? ` · ${t('lodging.home.shared')}` : ''}
                    </dd>
                  </div>
                ))}
            </dl>
          )}
        </Card>
      )}

      <Card>
        <h2 className="text-base font-semibold">{t('lodging.home.next')}</h2>
        {bookings.isPending ? (
          <Loading />
        ) : bookings.isError ? (
          <ProblemAlert problem={problemOf(bookings.error)} />
        ) : !next ? (
          <p className="mt-2 text-sm">{t('lodging.home.none')}</p>
        ) : (
          <Link
            to="/bookings/$bookingId"
            params={{ bookingId: next.id }}
            className="mt-2 block rounded focus-visible:outline-2"
            data-testid="next-booking"
          >
            <span className="flex items-baseline justify-between gap-3">
              <span className="text-sm font-medium">{names.get(next.propertyId) ?? '…'}</span>
              <Badge tone={bookingTone(next.status)}>
                {t(`lodging.booking.status.${next.status}`)}
              </Badge>
            </span>
            <span className="text-fg-muted mt-1 block text-sm">
              {stayDates(next.checkIn, next.checkOut)} ·{' '}
              {t('lodging.search.nights', { count: next.nights })}
            </span>
            <span className="mt-1 block font-mono text-sm tabular-nums">
              {t('lodging.receipt.member')}{' '}
              {money(next.quoteSnapshot.memberAmount, next.quoteSnapshot.currencyCode)}
            </span>
          </Link>
        )}
        {hold && hold !== next ? (
          <Link
            to="/bookings/$bookingId"
            params={{ bookingId: hold.id }}
            className="text-primary mt-3 block text-sm underline underline-offset-4"
          >
            {t('lodging.home.hold')}
          </Link>
        ) : null}
      </Card>

      {personId !== null ? (
        <Link to="/search" className={BUTTON_LINK} data-testid="home-search">
          {t('lodging.home.search')}
        </Link>
      ) : null}
    </div>
  );
}
