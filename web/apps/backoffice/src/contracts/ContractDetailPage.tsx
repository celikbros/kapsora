import {
  randomId,
  type ContractVersionSummary,
  type UpdateContractRequest,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  Dialog,
  EmptyState,
  FormField,
  Input,
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
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import {
  useContract,
  useContractVersions,
  useCreateContractVersion,
  useUpdateContract,
} from './queries';
import { CONTRACT_TRANSITIONS, versionFormSchema, type VersionFormValues } from './schema';

/** One contract with its version history. A version is where the prices actually live. */
export function ContractDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const { contractId } = useParams({ from: '/app/contracts/$contractId' });
  const canManage = usePermission('contract.manage');
  const contract = useContract(contractId);
  const versions = useContractVersions(contractId);
  const update = useUpdateContract(contractId);
  const createVersion = useCreateContractVersion(contractId);
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [versionKey, setVersionKey] = useState(randomId);

  const form = useForm<VersionFormValues>({
    resolver: zodResolver(versionFormSchema),
    defaultValues: {
      validFrom: '',
      validTo: '',
      currencyCode: 'TRY',
      notes: '',
      copyFromVersionId: '',
    },
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function changeStatus(status: string) {
    if (!contract.data || status === '') return;
    setProblem(null);
    const patch: UpdateContractRequest = { status: status as 'ACTIVE' | 'SUSPENDED' | 'CLOSED' };
    try {
      await update.mutateAsync({ etag: contract.data.etag, patch });
      toast.notify({ tone: 'success', title: t('contracts.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitVersion(values: VersionFormValues) {
    setProblem(null);
    try {
      const created = await createVersion.mutateAsync({
        body: {
          validFrom: values.validFrom,
          currencyCode: values.currencyCode,
          ...(values.validTo !== '' ? { validTo: values.validTo } : {}),
          ...(values.notes !== '' ? { notes: values.notes } : {}),
          ...(values.copyFromVersionId !== ''
            ? { copyFromVersionId: values.copyFromVersionId }
            : {}),
        },
        idempotencyKey: versionKey,
      });
      setAdding(false);
      setVersionKey(randomId());
      await navigate({
        to: '/contract-versions/$contractVersionId',
        params: { contractVersionId: created.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      setVersionKey(randomId());
    }
  }

  if (contract.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!contract.data) {
    return <ProblemAlert problem={problemOf(contract.error)} />;
  }

  const row = contract.data.data;
  const nextStatuses = CONTRACT_TRANSITIONS[row.status] ?? [];
  const history: ContractVersionSummary[] = [...(versions.data ?? [])].sort(
    (a, b) => b.versionNo - a.versionNo,
  );

  return (
    <>
      <PageHeader
        title={row.name}
        description={row.code}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('contracts.title'),
                render: (label) => (
                  <Link to="/contracts" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: row.name },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={statusTone(row.status)}>{t(`contracts.statuses.${row.status}`)}</Badge>
            {canManage ? (
              <Button size="sm" variant="secondary" onClick={() => setAdding(true)}>
                {t('contracts.versions.new')}
              </Button>
            ) : null}
          </>
        }
      />

      <ProblemAlert problem={problem} className="mb-4" />

      <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
        <Card>
          <h2 className="text-base font-semibold">{t('contracts.versions.title')}</h2>
          {history.length === 0 ? (
            <EmptyState title={t('contracts.versions.empty')} />
          ) : (
            <div className="mt-3">
              <Table data-testid="contract-version-table">
                <THead>
                  <TR>
                    <TH>{t('contracts.versions.columns.versionNo')}</TH>
                    <TH>{t('contracts.versions.columns.period')}</TH>
                    <TH>{t('contracts.versions.columns.currency')}</TH>
                    <TH>{t('contracts.versions.columns.published')}</TH>
                    <TH>{t('contracts.versions.columns.status')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {history.map((version) => (
                    <TR key={version.id}>
                      <TD>
                        <Link
                          to="/contract-versions/$contractVersionId"
                          params={{ contractVersionId: version.id }}
                          className="font-medium underline-offset-2 hover:underline"
                        >
                          {`v${version.versionNo}`}
                        </Link>
                      </TD>
                      <TD>
                        {formatDate(version.validFrom) || t('common.none')}
                        {version.validTo ? ` – ${formatDate(version.validTo)}` : ''}
                      </TD>
                      <TD>
                        <code className="font-mono text-xs">{version.currencyCode}</code>
                      </TD>
                      <TD>{formatDate(version.publishedAt) || t('common.none')}</TD>
                      <TD>
                        <Badge tone={statusTone(version.status)}>
                          {t(`contracts.versions.statuses.${version.status}`)}
                        </Badge>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </Card>

        <Card>
          <h2 className="text-base font-semibold">{t('contracts.detailTitle')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-fg-muted">{t('contracts.fields.provider')}</dt>
            <dd>{row.providerName ?? t('common.none')}</dd>
            <dt className="text-fg-muted">{t('contracts.fields.payer')}</dt>
            <dd>{row.payerName ?? t('common.none')}</dd>
            <dt className="text-fg-muted">{t('contracts.fields.sponsor')}</dt>
            <dd>{row.sponsorName ?? t('common.none')}</dd>
            <dt className="text-fg-muted">{t('contracts.fields.domain')}</dt>
            <dd>{t(`catalog.domains.${row.domainCode}`)}</dd>
          </dl>

          {canManage && nextStatuses.length > 0 ? (
            <div className="border-line mt-4 border-t pt-4">
              <FormField label={t('contracts.fields.status')}>
                <Select
                  name="status"
                  value=""
                  onChange={(e) => void changeStatus(e.target.value)}
                  placeholder={t('contracts.changeStatus')}
                  options={nextStatuses.map((status) => ({
                    value: status,
                    label: t(`contracts.statuses.${status}`),
                  }))}
                  disabled={update.isPending}
                />
              </FormField>
            </div>
          ) : null}
        </Card>
      </div>

      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) setAdding(false);
        }}
        title={t('contracts.versions.createTitle')}
      >
        <form onSubmit={form.handleSubmit(submitVersion)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('contracts.versions.fields.validFrom')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.validFrom)}
            >
              <Input {...form.register('validFrom')} type="date" />
            </FormField>
            <FormField
              label={t('contracts.versions.fields.validTo')}
              error={message(form.formState.errors.validTo)}
            >
              <Input {...form.register('validTo')} type="date" />
            </FormField>
            <FormField
              label={t('contracts.versions.fields.currency')}
              error={message(form.formState.errors.currencyCode)}
            >
              <Input {...form.register('currencyCode')} className="font-mono" maxLength={3} />
            </FormField>
            <FormField label={t('contracts.versions.copyFrom')}>
              <Select
                {...form.register('copyFromVersionId')}
                placeholder={t('contracts.versions.copyFromNone')}
                options={history.map((version) => ({
                  value: version.id,
                  label: `v${version.versionNo} · ${t(`contracts.versions.statuses.${version.status}`)}`,
                }))}
              />
            </FormField>
          </div>
          <FormField
            label={t('contracts.versions.fields.notes')}
            error={message(form.formState.errors.notes)}
          >
            <Textarea {...form.register('notes')} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setAdding(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={createVersion.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
