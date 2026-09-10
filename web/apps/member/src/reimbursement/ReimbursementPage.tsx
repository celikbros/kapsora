import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import { Badge, Button, Card, ProblemAlert, useMinWidth, useToast } from '@kapsora/ui';
import { Link, useParams } from '@tanstack/react-router';

import { Loading } from '../lodging/Loading';
import { Receipt, type ReceiptLine } from '../lodging/Receipt';
import { money } from '../lodging/words';
import { problemOf } from '../problems';
import {
  useDocument,
  useReimbursement,
  useServiceDefinitions,
  useSubmitReimbursement,
} from './queries';
import { reimbursementTone } from './words';

/**
 * One reimbursement as a receipt: what was asked, what was agreed, the account's last four
 * characters, and what happened when. A draft carries the one action, Gönder, which the
 * server accepts only once the receipt scanned clean.
 */
export function ReimbursementPage() {
  const { reimbursementId } = useParams({ from: '/app/reimbursements/$reimbursementId' });
  const { t } = useTranslation();
  const toast = useToast();
  const wide = useMinWidth(1024);
  const reimbursement = useReimbursement(reimbursementId);
  const submit = useSubmitReimbursement(reimbursementId);
  const definitions = useServiceDefinitions();
  const record = reimbursement.data?.data ?? null;
  const etag = reimbursement.data?.etag ?? '';
  const receipt = useDocument(
    record?.status === 'DRAFT' ? (record.receiptDocumentId ?? null) : null,
  );

  if (reimbursement.isPending) {
    return (
      <div className="p-4">
        <Loading />
      </div>
    );
  }
  if (reimbursement.isError) {
    return (
      <div className="p-4">
        <ProblemAlert problem={problemOf(reimbursement.error)} />
      </div>
    );
  }
  if (!record) return null;

  const scan = receipt.data?.data.scanStatus ?? null;
  const service = definitions.data?.items.find((d) => d.id === record.serviceDefinitionId)?.name;
  const lines: ReceiptLine[] = [
    { label: t('billing.member.service'), value: service ?? '…' },
    { label: t('billing.member.serviceDate'), value: formatDate(record.serviceDate) },
    {
      label: t('billing.member.requested'),
      value: money(record.requestedAmount, record.currencyCode),
      mono: true,
    },
    {
      label: t('billing.member.iban'),
      value: t('billing.member.ibanMasked', { tail: record.bankAccountMasked }),
      mono: true,
    },
  ];
  if (record.decisionReasonCode) {
    lines.push({ label: t('billing.member.reason'), value: record.decisionReasonCode, mono: true });
  }
  const total = record.approvedAmount
    ? {
        label: t('billing.member.approved'),
        amount: money(record.approvedAmount, record.currencyCode),
      }
    : undefined;

  const receiptCard = (
    <Receipt
      title={record.reference}
      lines={lines}
      total={total}
      action={
        record.status === 'DRAFT' ? (
          <div className="grid gap-2">
            {scan && scan !== 'CLEAN' ? (
              <p className="text-fg-muted text-sm" role="status" data-testid="receipt-scan">
                {scan === 'PENDING' || scan === 'SCANNING'
                  ? t('billing.member.receiptScanning')
                  : t(`documents.scan.${scan}`)}
              </p>
            ) : null}
            <ProblemAlert problem={submit.isError ? problemOf(submit.error) : null} />
            <Button
              onClick={() =>
                submit.mutate(etag, {
                  onSuccess: () =>
                    toast.notify({ tone: 'success', title: t('billing.member.submitted') }),
                })
              }
              loading={submit.isPending}
              disabled={scan !== null && scan !== 'CLEAN'}
              data-testid="submit-reimbursement"
            >
              {t('billing.member.submit')}
            </Button>
          </div>
        ) : undefined
      }
    />
  );

  return (
    <div className={wide ? 'grid grid-cols-[1fr_22rem] items-start gap-6 p-6' : 'grid gap-4 p-4'}>
      <div className="grid gap-4">
        <Link
          to="/reimbursements"
          className="text-primary text-sm underline-offset-4 hover:underline"
        >
          ← {t('billing.member.title')}
        </Link>
        <div className="flex items-baseline justify-between gap-3">
          <h1 className="font-mono text-base font-semibold">{record.reference}</h1>
          <Badge tone={reimbursementTone(record.status)} data-testid="reimbursement-status">
            {t(`billing.reimbursementStatus.${record.status}`)}
          </Badge>
        </div>

        {record.duplicateOfReference ? (
          <p
            className="border-warning bg-warning-soft rounded-md border px-3 py-2 text-sm"
            role="status"
          >
            {t('billing.member.duplicateWarning', { reference: record.duplicateOfReference })}
          </p>
        ) : null}

        {!wide ? receiptCard : null}

        <Card>
          <h2 className="text-base font-semibold">{t('billing.member.sequence')}</h2>
          <ol className="mt-2 grid gap-1 text-sm" data-testid="reimbursement-sequence">
            <li className="flex justify-between gap-3">
              <span>{t('billing.reimbursementStatus.DRAFT')}</span>
              <span className="text-fg-muted">{formatDateTime(record.createdAt)}</span>
            </li>
            {record.submittedAt ? (
              <li className="flex justify-between gap-3">
                <span>{t('billing.reimbursementStatus.SUBMITTED')}</span>
                <span className="text-fg-muted">{formatDateTime(record.submittedAt)}</span>
              </li>
            ) : null}
            {record.decidedAt ? (
              <li className="flex justify-between gap-3">
                <span>{t('billing.member.decided')}</span>
                <span className="text-fg-muted">{formatDateTime(record.decidedAt)}</span>
              </li>
            ) : null}
            {record.paidAt ? (
              <li className="flex justify-between gap-3">
                <span>
                  {t('billing.member.paidTo')} ·{' '}
                  <span className="font-mono text-xs">{record.paymentReference}</span>
                </span>
                <span className="text-fg-muted">{formatDateTime(record.paidAt)}</span>
              </li>
            ) : null}
          </ol>
        </Card>
      </div>
      {wide ? receiptCard : null}
    </div>
  );
}
