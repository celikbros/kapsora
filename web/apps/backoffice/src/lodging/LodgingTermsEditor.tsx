import type { LodgingPenaltyKind, PutLodgingTermsRequest } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  FormField,
  Input,
  ProblemAlert,
  Select,
  Spinner,
  useToast,
} from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useLodgingTerms, usePutLodgingTerms } from './queries';

interface Form {
  freeCancellationHoursBefore: string;
  penaltyKind: LodgingPenaltyKind;
  penaltyNights: string;
  penaltyPercent: string;
  noShowPercent: string;
  holdMinutes: string;
  minNights: string;
  maxNights: string;
  childFreeUnderAge: string;
}

const EMPTY: Form = {
  freeCancellationHoursBefore: '48',
  penaltyKind: 'NIGHTS',
  penaltyNights: '1',
  penaltyPercent: '',
  noShowPercent: '100',
  holdMinutes: '',
  minNights: '1',
  maxNights: '',
  childFreeUnderAge: '',
};

/**
 * The lodging terms of a contract version: what a cancellation costs, what a no-show costs,
 * how long a hold stands. Written on a draft, read everywhere else; a booking confirmed
 * under the version carries a copy, so the numbers here are the ones a fee is judged by
 * months later. Percentages are exact decimal strings and are never parsed here.
 */
export function LodgingTermsEditor({
  contractVersionId,
  versionEtag,
  editable,
}: {
  contractVersionId: string;
  versionEtag: string;
  editable: boolean;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const terms = useLodgingTerms(contractVersionId);
  const put = usePutLodgingTerms(contractVersionId);
  const [form, setForm] = useState<Form | null>(null);

  if (terms.isPending) return <Spinner />;
  if (terms.isError) return <ProblemAlert problem={problemOf(terms.error)} />;
  const current = terms.data?.data ?? null;

  const opened: Form =
    form ??
    (current
      ? {
          freeCancellationHoursBefore: String(current.freeCancellationHoursBefore),
          penaltyKind: current.penaltyKind,
          penaltyNights:
            current.penaltyNights === null || current.penaltyNights === undefined
              ? ''
              : String(current.penaltyNights),
          penaltyPercent: current.penaltyPercent ?? '',
          noShowPercent: current.noShowPercent,
          holdMinutes:
            current.holdMinutes === null || current.holdMinutes === undefined
              ? ''
              : String(current.holdMinutes),
          minNights: String(current.minNights),
          maxNights:
            current.maxNights === null || current.maxNights === undefined
              ? ''
              : String(current.maxNights),
          childFreeUnderAge:
            current.childFreeUnderAge === null || current.childFreeUnderAge === undefined
              ? ''
              : String(current.childFreeUnderAge),
        }
      : EMPTY);
  const set = (key: keyof Form) => (value: string) => setForm({ ...opened, [key]: value });

  function submit(e: FormEvent) {
    e.preventDefault();
    const body: PutLodgingTermsRequest = {
      freeCancellationHoursBefore: Number(opened.freeCancellationHoursBefore),
      penaltyKind: opened.penaltyKind,
      penaltyNights: opened.penaltyKind === 'NIGHTS' ? Number(opened.penaltyNights) : null,
      penaltyPercent: opened.penaltyKind === 'PERCENT' ? opened.penaltyPercent.trim() : null,
      noShowPercent: opened.noShowPercent.trim(),
      holdMinutes: opened.holdMinutes.trim() === '' ? null : Number(opened.holdMinutes),
      minNights: Number(opened.minNights),
      maxNights: opened.maxNights.trim() === '' ? null : Number(opened.maxNights),
      childFreeUnderAge:
        opened.childFreeUnderAge.trim() === '' ? null : Number(opened.childFreeUnderAge),
    };
    put.mutate(
      { etag: versionEtag, body },
      {
        onSuccess: () => {
          setForm(null);
          toast.notify({ tone: 'success', title: t('lodging.office.terms.saved') });
        },
      },
    );
  }

  return (
    <Card data-testid="lodging-terms">
      <h2 className="text-base font-semibold">{t('lodging.office.terms.title')}</h2>
      <p className="text-fg-muted mt-1 text-sm">{t('lodging.office.terms.intro')}</p>
      {!editable && !current ? (
        <p className="bg-warning-soft mt-3 rounded-md p-3 text-sm" data-testid="lodging-terms-none">
          {t('lodging.office.terms.none')}
        </p>
      ) : null}
      {!editable && current ? (
        <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-1 text-sm">
          <dt className="text-fg-muted">{t('lodging.office.terms.fields.freeHours')}</dt>
          <dd>{current.freeCancellationHoursBefore}</dd>
          <dt className="text-fg-muted">{t('lodging.office.terms.fields.penaltyKind')}</dt>
          <dd>
            {t(`lodging.office.terms.kinds.${current.penaltyKind}`)} ·{' '}
            {current.penaltyKind === 'NIGHTS'
              ? current.penaltyNights
              : `%${current.penaltyPercent}`}
          </dd>
          <dt className="text-fg-muted">{t('lodging.office.terms.fields.noShowPercent')}</dt>
          <dd>%{current.noShowPercent}</dd>
          <dt className="text-fg-muted">{t('lodging.office.terms.fields.minNights')}</dt>
          <dd>
            {current.minNights}
            {current.maxNights ? ` – ${current.maxNights}` : ''}
          </dd>
        </dl>
      ) : null}
      {!editable ? (
        <p className="text-fg-muted mt-2 text-xs">{t('lodging.office.terms.readOnly')}</p>
      ) : null}

      {editable ? (
        <form
          onSubmit={submit}
          className="mt-3 grid gap-3 md:grid-cols-3"
          noValidate
          data-testid="lodging-terms-form"
        >
          <FormField
            label={t('lodging.office.terms.fields.freeHours')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              type="number"
              inputMode="numeric"
              min={0}
              name="freeCancellationHoursBefore"
              value={opened.freeCancellationHoursBefore}
              onChange={(e) => set('freeCancellationHoursBefore')(e.target.value)}
            />
          </FormField>
          <FormField
            label={t('lodging.office.terms.fields.penaltyKind')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Select
              name="penaltyKind"
              value={opened.penaltyKind}
              onChange={(e) => set('penaltyKind')(e.target.value)}
              options={[
                { value: 'NIGHTS', label: t('lodging.office.terms.kinds.NIGHTS') },
                { value: 'PERCENT', label: t('lodging.office.terms.kinds.PERCENT') },
              ]}
            />
          </FormField>
          {opened.penaltyKind === 'NIGHTS' ? (
            <FormField
              label={t('lodging.office.terms.fields.penaltyNights')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                type="number"
                inputMode="numeric"
                min={0}
                name="penaltyNights"
                value={opened.penaltyNights}
                onChange={(e) => set('penaltyNights')(e.target.value)}
              />
            </FormField>
          ) : (
            <FormField
              label={t('lodging.office.terms.fields.penaltyPercent')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                inputMode="decimal"
                name="penaltyPercent"
                value={opened.penaltyPercent}
                onChange={(e) => set('penaltyPercent')(e.target.value)}
                className="font-mono"
              />
            </FormField>
          )}
          <FormField
            label={t('lodging.office.terms.fields.noShowPercent')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              inputMode="decimal"
              name="noShowPercent"
              value={opened.noShowPercent}
              onChange={(e) => set('noShowPercent')(e.target.value)}
              className="font-mono"
            />
          </FormField>
          <FormField label={t('lodging.office.terms.fields.holdMinutes')}>
            <Input
              type="number"
              inputMode="numeric"
              min={1}
              name="holdMinutes"
              value={opened.holdMinutes}
              onChange={(e) => set('holdMinutes')(e.target.value)}
            />
          </FormField>
          <FormField
            label={t('lodging.office.terms.fields.minNights')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              type="number"
              inputMode="numeric"
              min={1}
              name="minNights"
              value={opened.minNights}
              onChange={(e) => set('minNights')(e.target.value)}
            />
          </FormField>
          <FormField label={t('lodging.office.terms.fields.maxNights')}>
            <Input
              type="number"
              inputMode="numeric"
              min={1}
              name="maxNights"
              value={opened.maxNights}
              onChange={(e) => set('maxNights')(e.target.value)}
            />
          </FormField>
          <FormField label={t('lodging.office.terms.fields.childFreeUnderAge')}>
            <Input
              type="number"
              inputMode="numeric"
              min={0}
              max={18}
              name="childFreeUnderAge"
              value={opened.childFreeUnderAge}
              onChange={(e) => set('childFreeUnderAge')(e.target.value)}
            />
          </FormField>
          <div className="md:col-span-3">
            <ProblemAlert problem={put.isError ? problemOf(put.error) : null} className="mb-3" />
            <Button type="submit" loading={put.isPending}>
              {t('lodging.office.terms.save')}
            </Button>
          </div>
        </form>
      ) : null}
    </Card>
  );
}
