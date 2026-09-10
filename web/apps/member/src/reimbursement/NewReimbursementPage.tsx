import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  FormField,
  Input,
  ProblemAlert,
  Select,
  Spinner,
  useMinWidth,
} from '@kapsora/ui';
import { Link, useNavigate } from '@tanstack/react-router';
import { useRef, useState, type FormEvent } from 'react';

import { Receipt, type ReceiptLine } from '../lodging/Receipt';
import { money } from '../lodging/words';
import { problemOf } from '../problems';
import { useMyPersonId } from '../lodging/queries';
import {
  useCreateReimbursement,
  useCreateRequest,
  useDocument,
  useMyPerson,
  useProviders,
  useServiceDefinitions,
  useUploadReceipt,
} from './queries';
import { normalizeAccount } from './words';

/**
 * A reimbursement in three steps that fill one receipt: the request (what, when, where,
 * how much), the receipt (a file that is scanned before anything else happens), the
 * account (typed once, shown as four characters from then on). The receipt on the right
 * or under the thumb reads back what has been agreed so far; the one action moves the
 * member forward and is refused by the server, not guessed at here.
 */
export function NewReimbursementPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const wide = useMinWidth(1024);
  const personId = useMyPersonId();
  const me = useMyPerson();
  const definitions = useServiceDefinitions();
  const providers = useProviders();
  const createRequest = useCreateRequest();
  const upload = useUploadReceipt();
  const createReimbursement = useCreateReimbursement();

  const [serviceDefinitionId, setService] = useState('');
  const [serviceDate, setDate] = useState('');
  const [providerOrganizationId, setProvider] = useState('');
  const [amount, setAmount] = useState('');
  const [requestId, setRequestId] = useState<string | null>(null);
  const [enrollmentId, setEnrollmentId] = useState<string | null>(null);
  const [documentId, setDocumentId] = useState<string | null>(null);
  const [account, setAccount] = useState('');
  const fileRef = useRef<HTMLInputElement>(null);
  const document = useDocument(documentId);
  const scan = document.data?.data.scanStatus ?? null;
  const receiptReady = scan === 'CLEAN';

  const enrollments = (me.data?.enrollments ?? []).filter((e) => e.status === 'ACTIVE');
  const serviceName = definitions.data?.items.find((d) => d.id === serviceDefinitionId)?.name;
  const providerName = providers.data?.items.find(
    (p) => p.tenantOrganizationId === providerOrganizationId,
  )?.organizationName;

  function submitRequest(e: FormEvent) {
    e.preventDefault();
    if (!personId) return;
    // The enrollment that covers the service date; the server refuses one that does not.
    const enrollment =
      enrollments.find(
        (en) => en.validFrom <= serviceDate && (!en.validTo || en.validTo > serviceDate),
      ) ?? enrollments[0];
    if (!enrollment) return;
    createRequest.mutate(
      {
        requestType: 'REIMBURSEMENT',
        personId,
        enrollmentId: enrollment.id,
        providerOrganizationId,
        serviceDate,
        channel: 'MEMBER_PORTAL',
        items: [
          {
            serviceDefinitionId,
            unitType: 'MONEY',
            requestedQuantity: '1',
            requestedAmount: amount.trim(),
            currencyCode: 'TRY',
          },
        ],
      },
      {
        onSuccess: (created) => {
          setRequestId(created.data.id);
          setEnrollmentId(enrollment.id);
        },
      },
    );
  }

  function onFile(file: File | null) {
    if (!file || !requestId) return;
    upload.mutate({ file, requestId }, { onSuccess: (doc) => setDocumentId(doc.id) });
  }

  function submitAccount(e: FormEvent) {
    e.preventDefault();
    if (!requestId || !documentId) return;
    createReimbursement.mutate(
      {
        serviceRequestId: requestId,
        receiptDocumentId: documentId,
        requestedAmount: amount.trim(),
        bankAccount: normalizeAccount(account),
        currencyCode: 'TRY',
      },
      {
        onSuccess: (created) => {
          setAccount('');
          void navigate({
            to: '/reimbursements/$reimbursementId',
            params: { reimbursementId: created.data.id },
          });
        },
      },
    );
  }

  const lines: ReceiptLine[] = [];
  if (serviceName) lines.push({ label: t('billing.member.service'), value: serviceName });
  if (serviceDate) {
    lines.push({ label: t('billing.member.serviceDate'), value: formatDate(serviceDate) });
  }
  if (providerName) lines.push({ label: t('billing.member.provider'), value: providerName });
  if (documentId) {
    lines.push({
      label: t('billing.member.receipt'),
      value: scan ? t(`documents.scan.${scan}`) : '…',
    });
  }

  const step = !requestId ? 1 : !receiptReady ? 2 : 3;
  const unbound = personId === null || (me.isSuccess && enrollments.length === 0);

  return (
    <div
      className={wide ? 'grid grid-cols-[1fr_22rem] items-start gap-6 p-6' : 'grid gap-4 p-4 pb-4'}
    >
      <div className={wide ? 'grid gap-4' : 'grid gap-4 pb-64'}>
        <Link
          to="/reimbursements"
          className="text-primary text-sm underline-offset-4 hover:underline"
        >
          ← {t('billing.member.title')}
        </Link>
        <h1 className="text-xl font-semibold">{t('billing.member.new')}</h1>
        <ol className="flex gap-2 text-xs" aria-label={t('billing.member.new')} data-testid="steps">
          {(['request', 'receipt', 'account'] as const).map((key, i) => (
            <li
              key={key}
              aria-current={step === i + 1 ? 'step' : undefined}
              className={
                step === i + 1
                  ? 'bg-primary text-primary-fg rounded-full px-2.5 py-0.5'
                  : step > i + 1
                    ? 'bg-success-soft text-success rounded-full px-2.5 py-0.5'
                    : 'bg-surface-sunken text-fg-muted rounded-full px-2.5 py-0.5'
              }
            >
              {i + 1}. {t(`billing.member.step.${key}`)}
            </li>
          ))}
        </ol>

        {unbound ? (
          <Card>
            <p className="text-sm">{t('lodging.home.unbound')}</p>
          </Card>
        ) : null}

        <Card>
          <h2 className="text-base font-semibold">1. {t('billing.member.step.request')}</h2>
          <form
            onSubmit={submitRequest}
            className="mt-3 grid gap-3"
            noValidate
            data-testid="request-form"
          >
            <FormField
              label={t('billing.member.service')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              {definitions.isPending ? (
                <Spinner />
              ) : (
                <Select
                  name="serviceDefinitionId"
                  value={serviceDefinitionId}
                  onChange={(e) => setService(e.target.value)}
                  options={(definitions.data?.items ?? []).map((d) => ({
                    value: d.id,
                    label: d.name,
                  }))}
                  placeholder="—"
                  disabled={requestId !== null}
                />
              )}
            </FormField>
            <FormField
              label={t('billing.member.serviceDate')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                type="date"
                name="serviceDate"
                value={serviceDate}
                max={new Date().toISOString().slice(0, 10)}
                onChange={(e) => setDate(e.target.value)}
                disabled={requestId !== null}
                required
              />
            </FormField>
            <FormField
              label={t('billing.member.provider')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="providerOrganizationId"
                value={providerOrganizationId}
                onChange={(e) => setProvider(e.target.value)}
                options={(providers.data?.items ?? []).map((p) => ({
                  value: p.tenantOrganizationId,
                  label: p.organizationName,
                }))}
                placeholder="—"
                disabled={requestId !== null}
              />
            </FormField>
            <FormField
              label={t('billing.member.amount')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="amount"
                inputMode="decimal"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                className="font-mono"
                disabled={requestId !== null}
                required
              />
            </FormField>
            <ProblemAlert problem={createRequest.isError ? problemOf(createRequest.error) : null} />
            {!requestId ? (
              <Button
                type="submit"
                loading={createRequest.isPending}
                disabled={
                  unbound ||
                  serviceDefinitionId === '' ||
                  serviceDate === '' ||
                  providerOrganizationId === '' ||
                  amount.trim() === ''
                }
                data-testid="create-request"
              >
                {t('common.continue')}
              </Button>
            ) : null}
          </form>
        </Card>

        {requestId ? (
          <Card>
            <h2 className="text-base font-semibold">2. {t('billing.member.step.receipt')}</h2>
            <p className="text-fg-muted mt-1 text-sm">{t('billing.member.receiptHint')}</p>
            {!documentId ? (
              <div className="mt-3 grid gap-2">
                <input
                  ref={fileRef}
                  type="file"
                  accept="image/*,application/pdf"
                  aria-label={t('billing.member.receipt')}
                  onChange={(e) => onFile(e.target.files?.[0] ?? null)}
                  disabled={upload.isPending}
                  data-testid="receipt-file"
                  className="text-sm"
                />
                {upload.isPending ? (
                  <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
                    <Spinner /> {t('documents.uploading')}
                  </p>
                ) : null}
                <ProblemAlert problem={upload.isError ? problemOf(upload.error) : null} />
              </div>
            ) : (
              <p
                className="mt-3 flex items-center gap-2 text-sm"
                data-testid="receipt-scan"
                role="status"
              >
                {scan === 'PENDING' || scan === 'SCANNING' ? (
                  <>
                    <Spinner /> {t('billing.member.receiptScanning')}
                  </>
                ) : scan === 'CLEAN' ? (
                  <Badge tone="success">{t('billing.member.receiptClean')}</Badge>
                ) : scan ? (
                  <>
                    <Badge tone="danger">{t(`documents.scan.${scan}`)}</Badge>
                    <Button size="sm" variant="secondary" onClick={() => setDocumentId(null)}>
                      {t('documents.choose')}
                    </Button>
                  </>
                ) : (
                  '…'
                )}
              </p>
            )}
          </Card>
        ) : null}

        {receiptReady ? (
          <Card>
            <h2 className="text-base font-semibold">3. {t('billing.member.step.account')}</h2>
            <form
              onSubmit={submitAccount}
              className="mt-3 grid gap-3"
              noValidate
              data-testid="account-form"
            >
              <FormField
                label={t('billing.member.iban')}
                required
                requiredLabel={t('common.requiredMark')}
                hint={t('billing.member.ibanHint')}
              >
                <Input
                  name="bankAccount"
                  autoComplete="off"
                  value={account}
                  onChange={(e) => setAccount(e.target.value)}
                  className="font-mono"
                  required
                />
              </FormField>
              <ProblemAlert
                problem={createReimbursement.isError ? problemOf(createReimbursement.error) : null}
              />
              <Button
                type="submit"
                loading={createReimbursement.isPending}
                disabled={normalizeAccount(account).length < 15 || !enrollmentId}
                data-testid="create-reimbursement"
              >
                {t('billing.member.create')}
              </Button>
            </form>
          </Card>
        ) : null}
      </div>

      <Receipt
        title={t('billing.member.new')}
        lines={lines}
        total={
          amount.trim()
            ? { label: t('billing.member.amount'), amount: money(amount.trim(), 'TRY') }
            : undefined
        }
        arrivalKey={`${lines.length}`}
        pinned={!wide}
        {...(wide ? { className: 'sticky top-4' } : {})}
      />
    </div>
  );
}
