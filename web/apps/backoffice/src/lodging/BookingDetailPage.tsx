import type { Booking, LodgingPolicySnapshot, NoShowResult } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import {
  formatDate,
  formatDateTime,
  formatMoney,
  formatNumber,
  useTranslation,
} from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  FormField,
  HelpHint,
  PageHeader,
  ProblemAlert,
  Spinner,
  Textarea,
  useToast,
} from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';
import { useState, type ReactNode } from 'react';

import { usePersonName } from '../claims/names';
import { problemOf } from '../problems';
import { usePropertyNames, useRoomTypeName } from './names';
import { useBooking, useNoShow, useReviewNoShow } from './queries';
import { bookingTone, noShowTone } from './status';

/**
 * One booking laid out as the sequence that produced it: what was searched and shown,
 * what was held and until when, what the request decided, what was confirmed and under
 * which terms, what happened at the door, and what it cost. The no-show report, when there
 * is one, is reviewed here by a second person.
 */
export function BookingDetailPage() {
  const { bookingId } = useParams({ from: '/app/lodging/bookings/$bookingId' });
  const { t } = useTranslation();
  const booking = useBooking(bookingId);
  const record = booking.data?.data;
  const names = usePropertyNames();
  const roomName = useRoomTypeName(record?.propertyId ?? null, record?.roomTypeId ?? null);
  const personName = usePersonName(record?.personId);

  if (booking.isPending) return <Spinner />;
  if (booking.isError) return <ProblemAlert problem={problemOf(booking.error)} />;
  if (!record) return null;
  const q = record.quoteSnapshot;

  return (
    <div className="grid gap-4">
      <Breadcrumb
        items={[
          {
            label: t('lodging.office.bookings.title'),
            render: (text) => <Link to="/lodging/bookings">{text}</Link>,
          },
          { label: record.reference },
        ]}
      />
      <PageHeader
        title={`${t('lodging.office.bookings.detailTitle')} ${record.reference}`}
        actions={
          <Badge tone={bookingTone(record.status)} data-testid="booking-status">
            {t(`lodging.booking.status.${record.status}`)}
          </Badge>
        }
      />
      <p className="text-sm">
        {personName === undefined ? '…' : (personName ?? '—')} ·{' '}
        {names.get(record.propertyId) ?? '…'} · {roomName} · {formatDate(record.checkIn)} –{' '}
        {formatDate(record.checkOut)} · {t('lodging.search.nights', { count: record.nights })}
      </p>

      <ol className="grid gap-4" data-testid="booking-sequence">
        <Step title={t('lodging.office.bookings.sequence.searched')}>
          <Row
            label={t('lodging.office.bookings.labels.quotedAt')}
            value={formatDateTime(q.quotedAt)}
          />
          <Row
            label={t('lodging.office.bookings.labels.evaluation')}
            value={q.evaluationId ?? '—'}
            mono
          />
          <Row
            label={t('lodging.office.bookings.labels.covered')}
            value={`${q.coveredNights} / ${record.nights} ${t('lodging.unit.NIGHT')}`}
          />
          <Row
            label={t('lodging.office.bookings.labels.total')}
            value={formatMoney(q.totalAmount, q.currencyCode)}
            mono
          />
          <Row
            label={t('lodging.office.bookings.labels.payer')}
            value={formatMoney(q.payerAmount, q.currencyCode)}
            mono
          />
          <Row
            label={t('lodging.office.bookings.labels.member')}
            value={formatMoney(q.memberAmount, q.currencyCode)}
            mono
          />
          <Row
            label={t('lodging.office.bookings.labels.channel')}
            value={t(`lodging.office.bookings.channels.${record.channel}`)}
          />
          <Row
            label={t('lodging.office.bookings.labels.guests')}
            value={`${record.adults} + ${record.children}`}
          />
        </Step>
        <Step title={t('lodging.office.bookings.sequence.held')}>
          <Row label={t('lodging.booking.status.HOLD')} value={formatDateTime(record.createdAt)} />
          <Row
            label={t('lodging.office.bookings.labels.holdExpires')}
            value={record.holdExpiresAt ? formatDateTime(record.holdExpiresAt) : '—'}
          />
        </Step>
        <Step title={t('lodging.office.bookings.sequence.decided')}>
          <Row
            label={t('lodging.office.bookings.labels.request')}
            value={
              record.serviceRequestId ? (
                <Link
                  to="/requests/$requestId"
                  params={{ requestId: record.serviceRequestId }}
                  className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                >
                  {record.serviceRequestId}
                </Link>
              ) : (
                t('lodging.office.bookings.labels.pending')
              )
            }
          />
        </Step>
        <Step title={t('lodging.office.bookings.sequence.confirmed')}>
          <Row
            label={t('lodging.booking.status.CONFIRMED')}
            value={
              record.confirmedAt
                ? formatDateTime(record.confirmedAt)
                : t('lodging.office.bookings.labels.pending')
            }
          />
          <Row
            label={t('lodging.office.bookings.labels.authorization')}
            value={record.authorizationId ?? '—'}
            mono
          />
          <Row
            label={t('lodging.office.bookings.labels.voucher')}
            value={record.voucherId ? t('lodging.office.bookings.labels.voucherIssued') : '—'}
          />
          <PolicyRows policy={record.policySnapshot ?? null} />
        </Step>
        <Step title={t('lodging.office.bookings.sequence.door')}>
          <Row
            label={t('lodging.office.bookings.labels.checkedIn')}
            value={record.checkedInAt ? formatDateTime(record.checkedInAt) : '—'}
          />
          <Row
            label={t('lodging.office.bookings.labels.checkedOut')}
            value={record.checkedOutAt ? formatDateTime(record.checkedOutAt) : '—'}
          />
          <Row
            label={t('lodging.office.bookings.labels.actualNights')}
            value={
              record.actualNights === null || record.actualNights === undefined
                ? '—'
                : String(record.actualNights)
            }
          />
        </Step>
        <Step title={t('lodging.office.bookings.sequence.cost')}>
          <Row
            label={t('lodging.office.bookings.labels.cancelledAt')}
            value={record.cancelledAt ? formatDateTime(record.cancelledAt) : '—'}
          />
          <Row
            label={t('lodging.office.bookings.labels.reason')}
            value={record.cancelReasonCode ?? '—'}
            mono
          />
          <NoShowSection booking={record} />
        </Step>
      </ol>
    </div>
  );
}

function Step({ title, children }: { title: string; children: ReactNode }) {
  return (
    <li>
      <Card>
        <h2 className="text-base font-semibold">{title}</h2>
        <dl className="mt-2 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm [&>dd]:min-w-0 [&>dd]:break-words">
          {children}
        </dl>
      </Card>
    </li>
  );
}

function Row({ label, value, mono }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <>
      <dt className="text-fg-muted">{label}</dt>
      <dd className={mono ? 'font-mono text-xs tabular-nums' : ''}>{value}</dd>
    </>
  );
}

function NoShowSection({ booking }: { booking: Booking }) {
  const { t } = useTranslation();
  const noShow = useNoShow(booking.id);
  if (noShow.isPending) return null;
  if (noShow.isError) return <ProblemAlert problem={problemOf(noShow.error)} />;
  if (!noShow.data) {
    return (
      <>
        <dt className="text-fg-muted">{t('lodging.office.noShow.title')}</dt>
        <dd>{t('lodging.office.noShow.none')}</dd>
      </>
    );
  }
  return <NoShowReview booking={booking} result={noShow.data} />;
}

function NoShowReview({ booking, result }: { booking: Booking; result: NoShowResult }) {
  const { t } = useTranslation();
  const toast = useToast();
  const canReview = usePermission('accommodation.booking.manage');
  const review = useReviewNoShow(booking.id);
  const [comment, setComment] = useState('');
  const report = result.report;

  function decide(status: 'CONFIRMED' | 'DISPUTED' | 'REJECTED') {
    review.mutate(
      { status, ...(comment.trim() ? { comment: comment.trim() } : {}) },
      {
        onSuccess: () =>
          toast.notify({ tone: 'success', title: t('lodging.office.noShow.reviewed') }),
      },
    );
  }

  return (
    <>
      <dt className="text-fg-muted flex items-center gap-1.5">
        {t('lodging.office.noShow.title')}
        <HelpHint term="gelmeme" />
      </dt>
      <dd>
        <Badge tone={noShowTone(report.status)} data-testid="no-show-status">
          {t(`lodging.office.noShow.status.${report.status}`)}
        </Badge>
      </dd>
      <dt className="text-fg-muted">{t('lodging.office.noShow.reportedAt')}</dt>
      <dd>{formatDateTime(report.reportedAt)}</dd>
      <dt className="text-fg-muted">{t('lodging.office.noShow.assessed')}</dt>
      <dd className="font-mono tabular-nums">
        {formatMoney(report.assessedFeeAmount, report.currencyCode)}
      </dd>
      <dt className="text-fg-muted">{t('lodging.office.noShow.payer')}</dt>
      <dd className="font-mono tabular-nums">
        {formatMoney(report.payerAmount, report.currencyCode)}
      </dd>
      <dt className="text-fg-muted">{t('lodging.office.noShow.member')}</dt>
      <dd className="font-mono tabular-nums">
        {formatMoney(report.memberAmount, report.currencyCode)}
      </dd>
      <dt className="text-fg-muted">{t('lodging.office.noShow.consumedNights')}</dt>
      <dd>{report.consumedNights}</dd>
      {report.reviewedAt ? (
        <>
          <dt className="text-fg-muted">{t('lodging.office.noShow.reviewedAt')}</dt>
          <dd>{formatDateTime(report.reviewedAt)}</dd>
        </>
      ) : null}
      {report.status === 'REPORTED' && canReview ? (
        <>
          <dt className="text-fg-muted">{t('lodging.office.noShow.review')}</dt>
          <dd>
            <div className="grid gap-2" data-testid="no-show-review">
              <p className="text-fg-muted text-xs">{t('lodging.office.noShow.reviewHint')}</p>
              <FormField label={t('lodging.office.noShow.comment')}>
                <Textarea
                  name="comment"
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                  rows={2}
                />
              </FormField>
              <ProblemAlert problem={review.isError ? problemOf(review.error) : null} />
              <div className="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="danger"
                  onClick={() => decide('CONFIRMED')}
                  loading={review.isPending}
                >
                  {t('lodging.office.noShow.confirm')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => decide('DISPUTED')}
                  loading={review.isPending}
                >
                  {t('lodging.office.noShow.dispute')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => decide('REJECTED')}
                  loading={review.isPending}
                >
                  {t('lodging.office.noShow.reject')}
                </Button>
              </div>
            </div>
          </dd>
        </>
      ) : null}
    </>
  );
}

/** A percent the server sent as an exact string, shown for reading. */
function percent(value: string | null | undefined): string {
  if (!value) return '';
  const n = Number(value);
  return Number.isFinite(n) ? formatNumber(n, {}, Number.isInteger(n) ? 0 : 2) : value;
}

/** The policy the booking was confirmed under, as the payer's labelled rows. */
function PolicyRows({ policy }: { policy: LodgingPolicySnapshot | null }) {
  const { t } = useTranslation();
  const L = 'lodging.office.bookings.labels';
  if (!policy) return <Row label={t(`${L}.policy`)} value="—" />;
  return (
    <>
      <Row
        label={t(`${L}.policyFree`)}
        value={t(`${L}.policyFreeValue`, { hours: policy.freeCancellationHoursBefore })}
      />
      <Row
        label={t(`${L}.policyPenalty`)}
        value={
          policy.penaltyKind === 'NIGHTS'
            ? t(`${L}.policyPenaltyNights`, { count: policy.penaltyNights ?? 0 })
            : t(`${L}.policyPenaltyPercent`, { percent: percent(policy.penaltyPercent) })
        }
      />
      <Row
        label={t(`${L}.policyNoShow`)}
        value={t(`${L}.policyNoShowValue`, { percent: percent(policy.noShowPercent) })}
      />
    </>
  );
}
