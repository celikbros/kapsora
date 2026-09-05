import type { PriceItemInput, PriceList, PriceListInput, ReasonCommand } from '@kapsora/api-client';
import { usePermission, useSession, useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  Dialog,
  FormField,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useParams } from '@tanstack/react-router';
import { useMemo, useState } from 'react';
import { useForm } from 'react-hook-form';

import { useCategoryOptions, useDefinitionOptions } from '../catalogOptions';
import { parseIssueMessage, problemOf } from '../problems';
import { PriceItemsEditor, PriceItemsTable, type Option } from './PriceItemsEditor';
import { PriceListsEditor } from './PriceListsEditor';
import {
  useContract,
  useContractVersion,
  usePackageDefinitions,
  usePriceItems,
  useReplacePriceItems,
  useVersionCommands,
} from './queries';
import { RETIRE_REASONS, retireFormSchema, type RetireFormValues } from './schema';

/**
 * A contract version: the price sheet a quote, an authorization and a claim were priced
 * from. Status decides the page, because once published it may not move under a claim
 * that was paid against it.
 */
export function ContractVersionPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const { contractVersionId } = useParams({ from: '/app/contract-versions/$contractVersionId' });
  const actorId = useSession((s) => s.session?.actorId ?? null);
  const canManage = usePermission('contract.manage');
  const canPublish = usePermission('contract.publish');

  const version = useContractVersion(contractVersionId);
  const contract = useContract(version.data?.data.contractId ?? '');
  const commands = useVersionCommands(contractVersionId);
  const packages = usePackageDefinitions(contractVersionId);
  const definitions = useDefinitionOptions();
  const categories = useCategoryOptions();
  const stepUp = useStepUp();

  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [confirming, setConfirming] = useState<'submit' | 'publish' | null>(null);
  const [retiring, setRetiring] = useState(false);
  const [comment, setComment] = useState('');
  const [selectedListId, setSelectedListId] = useState('');

  const lists: PriceList[] = useMemo(
    () => version.data?.data.priceLists ?? [],
    [version.data?.data.priceLists],
  );
  const activeListId = selectedListId !== '' ? selectedListId : (lists[0]?.id ?? '');
  const activeList = lists.find((l) => l.id === activeListId);
  const items = usePriceItems(activeListId, { limit: 200 });
  const replaceItems = useReplacePriceItems(activeListId);

  const retireForm = useForm<RetireFormValues>({
    resolver: zodResolver(retireFormSchema),
    defaultValues: { reasonCode: 'SUPERSEDED', reasonText: '' },
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function savePriceLists(input: PriceListInput[]) {
    if (!version.data) return;
    setProblem(null);
    try {
      await commands.priceLists.mutateAsync({ etag: version.data.etag, items: input });
      toast.notify({ tone: 'success', title: t('priceLists.saved') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function savePriceItems(input: PriceItemInput[]) {
    if (!activeList) return;
    setProblem(null);
    try {
      await replaceItems.mutateAsync({ etag: String(activeList.rowVersion), items: input });
      toast.notify({ tone: 'success', title: t('priceItems.saved') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitForReview() {
    if (!version.data) return;
    setProblem(null);
    setConfirming(null);
    try {
      await commands.submit.mutateAsync({
        etag: version.data.etag,
        ...(comment !== '' ? { body: { comment } } : {}),
      });
      setComment('');
      toast.notify({ tone: 'success', title: t('contracts.versions.submitted') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function publish() {
    if (!version.data) return;
    setProblem(null);
    setConfirming(null);
    const etag = version.data.etag;
    try {
      await stepUp.run(() =>
        commands.publish.mutateAsync({
          etag,
          ...(comment !== '' ? { body: { comment } } : {}),
        }),
      );
      setComment('');
      toast.notify({ tone: 'success', title: t('contracts.versions.published') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function retire(values: RetireFormValues) {
    if (!version.data) return;
    setProblem(null);
    const etag = version.data.etag;
    const body: ReasonCommand = {
      reasonCode: values.reasonCode,
      ...(values.reasonText !== '' ? { reasonText: values.reasonText } : {}),
    };
    try {
      await stepUp.run(() => commands.retire.mutateAsync({ etag, body }));
      setRetiring(false);
      toast.notify({ tone: 'success', title: t('contracts.versions.retired') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  if (version.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!version.data) {
    return <ProblemAlert problem={problemOf(version.error)} />;
  }

  const row = version.data.data;
  const contractName = contract.data?.data.name;
  const isDraft = row.status === 'DRAFT';
  const isUnderReview = row.status === 'UNDER_REVIEW';
  const isPublished = row.status === 'PUBLISHED';
  const submittedByMe = row.submittedBy !== null && row.submittedBy === actorId;
  const busy =
    commands.submit.isPending ||
    commands.publish.isPending ||
    commands.retire.isPending ||
    stepUp.busy;

  const packageOptions: Option[] = (packages.data?.data ?? []).map((definition) => ({
    value: definition.id,
    label: `${definition.code} · ${definition.name}`,
  }));

  return (
    <>
      <PageHeader
        title={`${contractName ?? t('contracts.title')} · v${row.versionNo}`}
        description={
          row.validFrom
            ? `${formatDate(row.validFrom)}${row.validTo ? ` – ${formatDate(row.validTo)}` : ''} · ${row.currencyCode}`
            : row.currencyCode
        }
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
              {
                label: contractName ?? t('contracts.detailTitle'),
                render: (label) => (
                  <Link to="/contracts/$contractId" params={{ contractId: row.contractId }}>
                    {label}
                  </Link>
                ),
              },
              { label: `v${row.versionNo}` },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={statusTone(row.status)}>
              {t(`contracts.versions.statuses.${row.status}`)}
            </Badge>
            {isDraft && canManage ? (
              <Button onClick={() => setConfirming('submit')} disabled={busy}>
                {t('contracts.versions.submit')}
              </Button>
            ) : null}
            {isUnderReview && canPublish && !submittedByMe ? (
              <Button onClick={() => setConfirming('publish')} disabled={busy}>
                {t('contracts.versions.publish')}
              </Button>
            ) : null}
            {isPublished && canPublish ? (
              <Button variant="secondary" onClick={() => setRetiring(true)} disabled={busy}>
                {t('contracts.versions.retire')}
              </Button>
            ) : null}
          </>
        }
      />

      {isUnderReview && submittedByMe ? (
        <p role="status" className="bg-warning-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('contracts.versions.sameActorBlocked')}
        </p>
      ) : null}
      {isPublished || row.status === 'RETIRED' ? (
        <p className="text-fg-muted mb-4 text-sm">{t('contracts.versions.readOnly')}</p>
      ) : null}

      <ProblemAlert problem={problem} className="mb-4" />

      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('contracts.versions.detailTitle')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
            <dt className="text-fg-muted">{t('contracts.versions.fields.notes')}</dt>
            <dd>{row.notes || t('common.none')}</dd>
            {row.submittedBy ? (
              <>
                <dt className="text-fg-muted">{t('contracts.versions.fields.submittedBy')}</dt>
                <dd>{formatDate(row.submittedAt) || t('common.none')}</dd>
              </>
            ) : null}
            {row.publishedBy ? (
              <>
                <dt className="text-fg-muted">{t('contracts.versions.fields.publishedBy')}</dt>
                <dd>{formatDate(row.publishedAt) || t('common.none')}</dd>
              </>
            ) : null}
            {row.configurationHash ? (
              <>
                <dt className="text-fg-muted">
                  {t('contracts.versions.fields.configurationHash')}
                </dt>
                <dd>
                  <code className="font-mono text-xs">{row.configurationHash}</code>
                </dd>
              </>
            ) : null}
          </dl>
        </Card>

        {isDraft && canManage ? (
          <Card>
            <h2 className="mb-3 text-base font-semibold">{t('priceLists.title')}</h2>
            <PriceListsEditor
              lists={lists}
              onSave={savePriceLists}
              saving={commands.priceLists.isPending}
              problem={null}
            />
          </Card>
        ) : null}

        <Card>
          <div className="flex flex-wrap items-end justify-between gap-3">
            <h2 className="text-base font-semibold">{t('priceItems.title')}</h2>
            {lists.length > 1 ? (
              <FormField label={t('priceLists.title')}>
                <Select
                  name="priceList"
                  value={activeListId}
                  onChange={(e) => setSelectedListId(e.target.value)}
                  options={lists.map((list) => ({
                    value: list.id,
                    label: `${list.code} · ${list.name}`,
                  }))}
                  className="w-64"
                />
              </FormField>
            ) : null}
          </div>

          {lists.length === 0 ? (
            <p className="text-fg-muted mt-3 text-sm">{t('priceLists.empty')}</p>
          ) : items.isPending ? (
            <div className="text-fg-muted flex items-center gap-2 py-4 text-sm" aria-busy="true">
              <Spinner /> {t('common.loading')}
            </div>
          ) : isDraft && canManage ? (
            <div className="mt-3">
              <PriceItemsEditor
                key={activeListId}
                items={items.data?.data.items ?? []}
                definitions={definitions.data ?? []}
                categories={categories.data ?? []}
                packages={packageOptions}
                locations={[]}
                onSave={savePriceItems}
                saving={replaceItems.isPending}
                problem={null}
              />
            </div>
          ) : (
            <div className="mt-3">
              <PriceItemsTable
                items={items.data?.data.items ?? []}
                labelFor={(item) =>
                  definitions.data?.find((d) => d.value === item.serviceDefinitionId)?.label ??
                  categories.data?.find((c) => c.value === item.serviceCategoryId)?.label ??
                  packageOptions.find((p) => p.value === item.packageDefinitionId)?.label ??
                  t('common.none')
                }
              />
            </div>
          )}
        </Card>
      </div>

      <Dialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) setConfirming(null);
        }}
        title={
          confirming === 'publish'
            ? t('contracts.versions.publish')
            : t('contracts.versions.submit')
        }
        description={
          confirming === 'publish'
            ? t('contracts.versions.publishConfirm')
            : t('contracts.versions.submitConfirm')
        }
      >
        <div className="grid gap-4">
          <FormField label={t('contracts.versions.fields.reviewComment')}>
            <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setConfirming(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={busy}
              onClick={() => {
                if (confirming === 'publish') void publish();
                else void submitForReview();
              }}
            >
              {confirming === 'publish'
                ? t('contracts.versions.publish')
                : t('contracts.versions.submit')}
            </Button>
          </div>
        </div>
      </Dialog>

      <Dialog
        open={retiring}
        onOpenChange={(open) => {
          if (!open) setRetiring(false);
        }}
        title={t('contracts.versions.retire')}
        description={t('contracts.versions.retireConfirm')}
      >
        <form onSubmit={retireForm.handleSubmit(retire)} className="grid gap-4" noValidate>
          <FormField
            label={t('contracts.versions.retireReason')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(retireForm.formState.errors.reasonCode)}
          >
            <Select
              {...retireForm.register('reasonCode')}
              options={RETIRE_REASONS.map((reason) => ({
                value: reason,
                label: t(`plans.versions.retireReasons.${reason}`),
              }))}
            />
          </FormField>
          <FormField
            label={t('contracts.versions.fields.reviewComment')}
            error={message(retireForm.formState.errors.reasonText)}
          >
            <Textarea {...retireForm.register('reasonText')} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setRetiring(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={busy}>
              {t('contracts.versions.retire')}
            </Button>
          </div>
        </form>
      </Dialog>

      <StepUpDialog
        open={stepUp.required}
        action={t('contracts.versions.publish')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
