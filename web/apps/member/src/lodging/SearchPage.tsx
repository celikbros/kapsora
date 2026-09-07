import type { AvailabilityRoomTypeResult, AvailabilitySearchResult } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  FormField,
  Input,
  ProblemAlert,
  Select,
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { Receipt, type ReceiptLine } from './Receipt';
import { useHold, useJoinWaitlist, useProperties, useSearch } from './queries';
import { money, quantity, stayDates, unitWord } from './words';

interface SearchForm {
  checkIn: string;
  checkOut: string;
  adults: string;
  children: string;
  where: string;
}

const EMPTY: SearchForm = { checkIn: '', checkOut: '', adults: '2', children: '0', where: '' };

/**
 * Search: dates, guests, where; the rooms answer with their nights and "Ödeyeceğiniz"; the
 * receipt fills in the moment a room is chosen and carries the one action, Odayı tut.
 *
 * Nothing is computed here. `nights`, `available`, the quote and `coveredNights` are the
 * server's; a room the plan does not fully carry says so on its own line rather than
 * hiding, and a room with no price says why.
 */
export function SearchPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const toast = useToast();
  const wide = useMinWidth(1024);
  const properties = useProperties();
  const search = useSearch();
  const hold = useHold();
  const join = useJoinWaitlist();
  const [form, setForm] = useState<SearchForm>(EMPTY);
  const [chosen, setChosen] = useState<AvailabilityRoomTypeResult | null>(null);

  const set = (key: keyof SearchForm) => (value: string) =>
    setForm((f) => ({ ...f, [key]: value }));

  const regions = Array.from(
    new Set(
      (properties.data?.items ?? []).map((p) => p.regionCode).filter((r): r is string => !!r),
    ),
  );
  const whereOptions = [
    ...regions.map((r) => ({
      value: `region:${r}`,
      label: `${r} · ${t('lodging.search.anyProperty')}`,
    })),
    ...(properties.data?.items ?? []).map((p) => ({ value: `property:${p.id}`, label: p.name })),
  ];

  function submit(e: FormEvent) {
    e.preventDefault();
    setChosen(null);
    const [kind, id] = form.where.split(':', 2);
    search.mutate({
      checkIn: form.checkIn,
      checkOut: form.checkOut,
      adults: Number(form.adults),
      children: Number(form.children),
      ...(kind === 'property' ? { propertyId: id } : {}),
      ...(kind === 'region' ? { regionCode: id } : {}),
    });
  }

  const result: AvailabilitySearchResult | undefined = search.data;
  const canSearch =
    form.checkIn !== '' && form.checkOut !== '' && form.where !== '' && Number(form.adults) >= 1;

  function holdChosen() {
    if (!result || !chosen) return;
    hold.mutate(
      {
        roomTypeId: chosen.roomType.id,
        checkIn: result.checkIn,
        checkOut: result.checkOut,
        adults: Number(form.adults),
        children: Number(form.children),
        channel: 'MEMBER_PORTAL',
      },
      {
        onSuccess: (booking) =>
          void navigate({ to: '/bookings/$bookingId', params: { bookingId: booking.data.id } }),
      },
    );
  }

  function joinWaitlist(room: AvailabilityRoomTypeResult) {
    if (!result) return;
    join.mutate(
      {
        propertyId: room.property.id,
        roomTypeId: room.roomType.id,
        checkIn: result.checkIn,
        checkOut: result.checkOut,
        adults: Number(form.adults),
        children: Number(form.children),
      },
      {
        onSuccess: () =>
          toast.notify({ tone: 'success', title: t('lodging.search.waitlistJoined') }),
      },
    );
  }

  const receiptLines: ReceiptLine[] =
    result && chosen
      ? [
          { label: t('lodging.receipt.dates'), value: stayDates(result.checkIn, result.checkOut) },
          {
            label: t('lodging.receipt.room'),
            value: `${chosen.property.name} · ${chosen.roomType.name}`,
          },
          {
            label: t('lodging.receipt.nights'),
            value: t('lodging.search.nights', { count: result.nights }),
          },
          ...(chosen.quote
            ? [
                {
                  label: t('lodging.receipt.total'),
                  value: money(chosen.quote.totalAmount, chosen.quote.currencyCode),
                  mono: true,
                },
                {
                  label: t('lodging.receipt.plan'),
                  value: money(chosen.quote.payerAmount, chosen.quote.currencyCode),
                  mono: true,
                },
              ]
            : []),
        ]
      : [];

  const receipt = (
    <Receipt
      title={t('lodging.receipt.title')}
      lines={receiptLines}
      total={
        chosen?.quote
          ? {
              label: t('lodging.receipt.member'),
              amount: money(chosen.quote.memberAmount, chosen.quote.currencyCode),
            }
          : undefined
      }
      pinned={!wide}
      arrivalKey={chosen?.roomType.id}
      className={wide ? 'sticky top-20' : ''}
      action={
        chosen ? (
          <>
            <ProblemAlert problem={hold.isError ? problemOf(hold.error) : null} />
            <Button onClick={holdChosen} loading={hold.isPending} disabled={!chosen.quote}>
              {hold.isPending ? t('lodging.receipt.holding') : t('lodging.receipt.hold')}
            </Button>
          </>
        ) : (
          <p className="text-fg-muted text-sm">{t('lodging.receipt.empty')}</p>
        )
      }
    />
  );

  const results = (
    <section
      aria-labelledby="results-heading"
      // On a phone the receipt is pinned over the bottom of the list; the last row needs
      // room to scroll clear of it once there is a receipt to pin.
      className={wide ? 'grid gap-3' : chosen ? 'grid gap-3 pb-80' : 'grid gap-3'}
    >
      {result ? (
        <>
          <h2 id="results-heading" className="text-base font-semibold">
            {t('lodging.search.results')} · {t('lodging.search.nights', { count: result.nights })}
          </h2>
          {result.entitlement ? (
            <p className="text-fg-muted text-sm" data-testid="entitlement-line">
              {t('lodging.search.remaining', {
                remaining: quantity(result.entitlement.remaining),
                unit: unitWord(t, result.entitlement.unit),
              })}
            </p>
          ) : null}
          {result.results.length === 0 ? (
            <p className="text-sm">{t('lodging.search.none')}</p>
          ) : (
            <ul className="grid gap-2" data-testid="room-list">
              {result.results.map((room) => (
                <RoomRow
                  key={room.roomType.id}
                  room={room}
                  nights={result.nights}
                  chosen={chosen?.roomType.id === room.roomType.id}
                  onChoose={() => setChosen(room)}
                  onWaitlist={() => joinWaitlist(room)}
                  joining={join.isPending}
                />
              ))}
            </ul>
          )}
        </>
      ) : (
        <p className="text-fg-muted text-sm">{t('lodging.search.empty')}</p>
      )}
    </section>
  );

  return (
    <div
      className={wide ? 'grid grid-cols-[1fr_22rem] items-start gap-6 p-6' : 'grid gap-4 p-4 pb-4'}
    >
      <div className="grid gap-4">
        <h1 className="text-xl font-semibold">{t('lodging.search.title')}</h1>
        <Card>
          <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2" noValidate>
            <FormField
              label={t('lodging.search.checkIn')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                type="date"
                name="checkIn"
                value={form.checkIn}
                onChange={(e) => set('checkIn')(e.target.value)}
                required
              />
            </FormField>
            <FormField
              label={t('lodging.search.checkOut')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                type="date"
                name="checkOut"
                value={form.checkOut}
                min={form.checkIn}
                onChange={(e) => set('checkOut')(e.target.value)}
                required
              />
            </FormField>
            <FormField
              label={t('lodging.search.adults')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                type="number"
                name="adults"
                inputMode="numeric"
                min={1}
                max={20}
                value={form.adults}
                onChange={(e) => set('adults')(e.target.value)}
                required
              />
            </FormField>
            <FormField label={t('lodging.search.children')}>
              <Input
                type="number"
                name="children"
                inputMode="numeric"
                min={0}
                max={20}
                value={form.children}
                onChange={(e) => set('children')(e.target.value)}
              />
            </FormField>
            <div className="sm:col-span-2">
              <FormField
                label={t('lodging.search.where')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Select
                  name="where"
                  value={form.where}
                  onChange={(e) => set('where')(e.target.value)}
                  options={whereOptions}
                  placeholder={t('lodging.search.property')}
                />
              </FormField>
            </div>
            <div className="sm:col-span-2">
              <ProblemAlert problem={search.isError ? problemOf(search.error) : null} />
              <Button type="submit" loading={search.isPending} disabled={!canSearch}>
                {t('lodging.search.submit')}
              </Button>
            </div>
          </form>
        </Card>
        {results}
      </div>
      {receipt}
    </div>
  );
}

function RoomRow({
  room,
  nights,
  chosen,
  onChoose,
  onWaitlist,
  joining,
}: {
  room: AvailabilityRoomTypeResult;
  nights: number;
  chosen: boolean;
  onChoose: () => void;
  onWaitlist: () => void;
  joining: boolean;
}) {
  const { t } = useTranslation();
  const full = room.available === 0;
  return (
    <li
      className={`bg-surface border-border rounded-lg border p-3 ${chosen ? 'ring-primary ring-2' : ''}`}
      data-testid="room-row"
      aria-current={chosen ? 'true' : undefined}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm font-medium">{room.property.name}</p>
          <p className="text-fg-muted text-sm">{room.roomType.name}</p>
          <p className="text-fg-muted text-xs">
            {t('lodging.search.occupancy', {
              adults: room.roomType.maxAdults,
              children: room.roomType.maxChildren,
            })}
          </p>
        </div>
        <p className="text-right text-sm">
          {full ? (
            <span className="text-danger">{t('lodging.search.full')}</span>
          ) : (
            t('lodging.search.available', { count: room.available })
          )}
        </p>
      </div>
      <div className="mt-2 flex items-end justify-between gap-3">
        <div className="text-sm">
          {room.quote ? (
            <CoverageLine room={room} nights={nights} />
          ) : (
            <p>
              <span className="text-fg-muted">{t('lodging.search.noQuote')}</span>
              {room.quoteUnavailableReason ? (
                <span className="text-fg-muted block text-xs">
                  {t(`lodging.search.noQuoteReason.${room.quoteUnavailableReason}`)}
                </span>
              ) : null}
            </p>
          )}
        </div>
        {room.quote ? (
          <p className="text-right">
            <span className="text-fg-muted block text-xs">{t('lodging.receipt.member')}</span>
            <span
              className="font-mono text-base font-semibold whitespace-nowrap tabular-nums"
              data-testid="room-member-amount"
            >
              {money(room.quote.memberAmount, room.quote.currencyCode)}
            </span>
          </p>
        ) : null}
      </div>
      <div className="mt-3">
        {full ? (
          <Button size="sm" variant="secondary" onClick={onWaitlist} loading={joining}>
            {t('lodging.search.waitlist')}
          </Button>
        ) : (
          <Button
            size="sm"
            variant={chosen ? 'secondary' : 'primary'}
            onClick={onChoose}
            disabled={!room.quote}
            aria-pressed={chosen}
          >
            {chosen ? t('lodging.search.chosen') : t('lodging.search.choose')}
          </Button>
        )}
      </div>
    </li>
  );
}

/**
 * How much of this stay the plan carries, from the quote's own figures: a night the plan
 * pays for has a payer amount; the count of such nights is read, never derived from money.
 */
function CoverageLine({ room, nights }: { room: AvailabilityRoomTypeResult; nights: number }) {
  const { t } = useTranslation();
  const quote = room.quote;
  if (!quote) return null;
  const covered = quote.nightlyAmounts.filter(
    (n) => n.payerAmount !== '0' && !/^0(\.0+)?$/.test(n.payerAmount),
  ).length;
  if (covered === 0) return <p className="text-warning">{t('lodging.search.coveredNone')}</p>;
  if (covered >= nights) return <p className="text-success">{t('lodging.search.coveredAll')}</p>;
  return (
    <p className="text-warning">
      {t('lodging.search.coveredSome', { covered, rest: nights - covered })}
    </p>
  );
}
