import type { Booking } from '@kapsora/api-client';
import { useStepUp } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  HelpHint,
  ProblemAlert,
  Spinner,
  StepUpDialog,
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState } from 'react';

import { problemOf } from '../problems';
import { Countdown } from './Countdown';
import { Receipt, type ReceiptLine } from './Receipt';
import { usePropertyNames, useRoomTypeName } from './names';
import {
  useBooking,
  useCancel,
  useCancellationPreview,
  useConfirm,
  useRelease,
  useVoucher,
} from './queries';
import { bookingTone, cancellationSentences, money, policySentences, stayDates } from './words';

/**
 * One booking, as the receipt it was made from and the state it is in.
 *
 * HOLD is the confirmation screen: the receipt, the server's countdown, and Onayla with the
 * amount on its own line. CONFIRMED is the door: the voucher shown once on request, and
 * "what do I pay if I cancel" answered before İptal et is offered. Everything after that is
 * read as what happened.
 */
export function BookingPage() {
  const { bookingId } = useParams({ from: '/app/bookings/$bookingId' });
  const { t } = useTranslation();
  const wide = useMinWidth(1024);
  const booking = useBooking(bookingId, true);
  const record = booking.data?.data;
  const names = usePropertyNames();
  const roomName = useRoomTypeName(record?.propertyId ?? null, record?.roomTypeId ?? null);
  const [expired, setExpired] = useState(false);

  if (booking.isPending) return <Spinner />;
  if (booking.isError) {
    return (
      <div className="p-4">
        <ProblemAlert problem={problemOf(booking.error)} />
      </div>
    );
  }
  if (!record) return null;

  const snapshot = record.quoteSnapshot;
  const currency = snapshot.currencyCode;
  const lines: ReceiptLine[] = [
    { label: t('lodging.receipt.dates'), value: stayDates(record.checkIn, record.checkOut) },
    {
      label: t('lodging.receipt.room'),
      value: `${names.get(record.propertyId) ?? '…'} · ${roomName}`,
    },
    {
      label: t('lodging.receipt.nights'),
      value: t('lodging.search.nights', { count: record.nights }),
      note:
        snapshot.coveredNights < record.nights
          ? t('lodging.search.coveredSome', {
              covered: snapshot.coveredNights,
              rest: record.nights - snapshot.coveredNights,
            })
          : t('lodging.search.coveredAll'),
    },
    { label: t('lodging.receipt.guests'), value: `${record.adults} + ${record.children}` },
    { label: t('lodging.receipt.total'), value: money(snapshot.totalAmount, currency), mono: true },
    { label: t('lodging.receipt.plan'), value: money(snapshot.payerAmount, currency), mono: true },
  ];
  const total = {
    label: t('lodging.receipt.member'),
    amount: money(snapshot.memberAmount, currency),
  };
  // The countdown reads the server's deadline and says when it has passed; nothing here
  // reads a clock during render.
  const holdExpired = record.status === 'HOLD' && expired;
  const status = holdExpired ? 'EXPIRED' : record.status;

  const header = (
    <header className="grid gap-1">
      <div className="flex items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">{t('lodging.booking.title')}</h1>
        <Badge tone={bookingTone(status)} data-testid="booking-status">
          {t(`lodging.booking.status.${status}`)}
        </Badge>
      </div>
      <p className="text-fg-muted font-mono text-xs">{record.reference}</p>
    </header>
  );

  let body: React.ReactNode;
  switch (status) {
    case 'HOLD':
      body = <HoldPanel record={record} onExpired={() => setExpired(true)} />;
      break;
    case 'EXPIRED':
      body = (
        <Card>
          <p className="text-sm font-medium">{t('lodging.receipt.expired')}</p>
          <p className="text-fg-muted mt-1 text-sm">{t('lodging.receipt.expiredBody')}</p>
          <Link
            to="/search"
            className="bg-primary text-primary-fg hover:bg-primary-hover mt-3 inline-flex items-center justify-center rounded-md px-4 py-2 text-sm font-medium"
          >
            {t('lodging.receipt.backToSearch')}
          </Link>
        </Card>
      );
      break;
    case 'PENDING_APPROVAL':
      body = (
        <Card>
          <p className="text-sm">{t('lodging.booking.pendingBody')}</p>
        </Card>
      );
      break;
    case 'CONFIRMED':
      body = <ConfirmedPanel record={record} />;
      break;
    default:
      body = <HistoryPanel record={record} />;
  }

  const receipt = (
    <Receipt
      title={t('lodging.receipt.title')}
      lines={lines}
      total={total}
      className={wide ? 'sticky top-20' : ''}
    >
      {record.policySnapshot ? (
        <div className="mt-3">
          <h3 className="text-fg-muted text-xs font-medium tracking-[0.02em] uppercase">
            {t('lodging.receipt.policy')}
          </h3>
          <ul className="mt-1 grid gap-0.5 text-sm">
            {policySentences(t, record.policySnapshot).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </div>
      ) : null}
    </Receipt>
  );

  return (
    <div className={wide ? 'grid grid-cols-[1fr_22rem] items-start gap-6 p-6' : 'grid gap-4 p-4'}>
      <div className="grid gap-4">
        {header}
        {body}
        {!wide ? receipt : null}
      </div>
      {wide ? receipt : null}
    </div>
  );
}

function HoldPanel({ record, onExpired }: { record: Booking; onExpired: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const stepUp = useStepUp();
  const confirm = useConfirm(record.id);
  const release = useRelease(record.id);
  const currency = record.quoteSnapshot.currencyCode;

  async function onConfirm() {
    const result = await stepUp.run(() => confirm.mutateAsync());
    if (result?.data.status === 'CONFIRMED') {
      toast.notify({ tone: 'success', title: t('lodging.booking.confirmedBody') });
    }
  }

  return (
    <Card>
      {record.holdExpiresAt ? (
        <Countdown
          expiresAt={record.holdExpiresAt}
          onExpired={onExpired}
          label={t('lodging.receipt.expiresIn')}
        />
      ) : null}
      <p className="text-fg-muted mt-1 flex items-center gap-1.5 text-xs">
        <span>
          {t('lodging.receipt.expiresAt')}: {formatDateTime(record.holdExpiresAt ?? null)}
        </span>
        <HelpHint term="tutma" />
      </p>
      <p className="text-fg-muted mt-3 text-sm">{t('lodging.receipt.policyAtConfirm')}</p>
      <ProblemAlert problem={confirm.isError ? problemOf(confirm.error) : null} />
      <div className="mt-4 grid gap-2">
        <Button
          onClick={() => void onConfirm()}
          loading={confirm.isPending}
          data-testid="confirm-button"
        >
          <span className="grid text-center leading-tight">
            <span>
              {confirm.isPending ? t('lodging.receipt.confirming') : t('lodging.receipt.confirm')}
            </span>
            <span className="font-mono text-xs font-normal tabular-nums">
              {t('lodging.receipt.payLine', {
                amount: money(record.quoteSnapshot.memberAmount, currency),
              })}
            </span>
          </span>
        </Button>
        <Button
          variant="secondary"
          onClick={() =>
            release.mutate(undefined, {
              onSuccess: () => toast.notify({ tone: 'info', title: t('lodging.receipt.released') }),
            })
          }
          loading={release.isPending}
        >
          {t('lodging.receipt.release')}
        </Button>
      </div>
      <StepUpDialog
        open={stepUp.required}
        action={t('lodging.receipt.confirm')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </Card>
  );
}

function ConfirmedPanel({ record }: { record: Booking }) {
  const { t } = useTranslation();
  const toast = useToast();
  const voucher = useVoucher(record.id);
  const preview = useCancellationPreview(record.id);
  const cancel = useCancel(record.id);
  const [shown, setShown] = useState(false);

  return (
    <div className="grid gap-4">
      <Card>
        <p className="text-sm">{t('lodging.booking.confirmedBody')}</p>
      </Card>

      <Card>
        <div className="flex items-center gap-1.5">
          <h2 className="text-base font-semibold">{t('lodging.booking.voucher')}</h2>
          <HelpHint term="kupon" />
        </div>
        <p className="text-fg-muted mt-1 text-sm">{t('lodging.booking.voucherOnce')}</p>
        <ProblemAlert problem={voucher.isError ? problemOf(voucher.error) : null} />
        {voucher.data && shown ? (
          <div className="mt-3 grid gap-2" data-testid="voucher-panel">
            <p className="text-fg-muted text-xs">{t('lodging.booking.voucherCode')}</p>
            <p
              className="font-mono text-2xl font-semibold tracking-wider"
              data-testid="voucher-token"
            >
              {voucher.data.token}
            </p>
            <p className="text-fg-muted text-xs">
              {t('lodging.booking.voucherValid')}:{' '}
              {stayDates(voucher.data.validFrom, voucher.data.validTo)}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" onClick={() => setShown(false)}>
                {t('lodging.booking.voucherHidden')}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => voucher.mutate()}
                loading={voucher.isPending}
              >
                {t('lodging.booking.voucherReissue')}
              </Button>
            </div>
          </div>
        ) : (
          <Button
            className="mt-3"
            size="sm"
            onClick={() => {
              setShown(true);
              if (!voucher.data) voucher.mutate();
            }}
            loading={voucher.isPending}
          >
            {voucher.data ? t('lodging.booking.voucherShow') : t('lodging.booking.voucherShow')}
          </Button>
        )}
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('lodging.booking.cancel')}</h2>
        <ProblemAlert
          problem={
            preview.isError
              ? problemOf(preview.error)
              : cancel.isError
                ? problemOf(cancel.error)
                : null
          }
        />
        {preview.data ? (
          <div className="mt-2 grid gap-2" data-testid="cancellation-preview">
            <ul className="grid gap-0.5 text-sm">
              {cancellationSentences(t, preview.data.quote).map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ul>
            <div className="flex flex-wrap gap-2">
              <Button
                variant="danger"
                size="sm"
                onClick={() =>
                  cancel.mutate(undefined, {
                    onSuccess: (result) =>
                      toast.notify({
                        tone: 'info',
                        title: result.quote.free
                          ? t('lodging.booking.cancelled')
                          : t('lodging.booking.cancelledFee', {
                              amount: money(result.quote.memberFee, result.quote.currencyCode),
                            }),
                      }),
                  })
                }
                loading={cancel.isPending}
              >
                {t('lodging.booking.cancelConfirm')}
              </Button>
              <Button variant="secondary" size="sm" onClick={() => preview.reset()}>
                {t('lodging.booking.keep')}
              </Button>
            </div>
          </div>
        ) : (
          <Button
            className="mt-2"
            size="sm"
            variant="secondary"
            onClick={() => preview.mutate()}
            loading={preview.isPending}
          >
            {t('lodging.booking.cancelPreview')}
          </Button>
        )}
      </Card>
    </div>
  );
}

/** What happened, in order, for a booking nobody can change any more. */
function HistoryPanel({ record }: { record: Booking }) {
  const { t } = useTranslation();
  const events: { label: string; at: string | null | undefined }[] = [
    { label: t('lodging.booking.status.CONFIRMED'), at: record.confirmedAt },
    { label: t('lodging.booking.checkedIn'), at: record.checkedInAt },
    { label: t('lodging.booking.checkedOut'), at: record.checkedOutAt },
    { label: t('lodging.booking.status.CANCELLED'), at: record.cancelledAt },
  ].filter((e) => !!e.at);
  return (
    <Card>
      {record.status === 'NO_SHOW' ? (
        <p className="text-sm">{t('lodging.booking.noShow')}</p>
      ) : null}
      {record.status === 'COMPLETED' &&
      record.actualNights !== null &&
      record.actualNights !== undefined ? (
        <p className="text-sm">
          {t('lodging.booking.actualNights', { count: record.actualNights })}
        </p>
      ) : null}
      {events.length > 0 ? (
        <>
          <h2 className="text-fg-muted mt-2 text-xs font-medium tracking-[0.02em] uppercase">
            {t('lodging.booking.sequence')}
          </h2>
          <ol className="mt-1 grid gap-1 text-sm">
            {events.map((e) => (
              <li key={e.label} className="flex justify-between gap-3">
                <span>{e.label}</span>
                <span className="text-fg-muted">{formatDateTime(e.at ?? null)}</span>
              </li>
            ))}
          </ol>
        </>
      ) : null}
    </Card>
  );
}
