import type { CodeValue, Diagnosis, DiagnosisInput, DiagnosisType } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { Badge, Button, FormField, Input, ProblemAlert, Select, useToast } from '@kapsora/ui';
import { useState } from 'react';

import { problemOf } from '../problems';
import { isSensitiveCode, useIcd10Search, usePutDiagnoses } from './queries';

interface Chosen {
  codeValueId: string;
  code: string;
  display: string;
  sensitive: boolean;
  diagnosisType: DiagnosisType;
}

const TYPES: DiagnosisType[] = ['PRIMARY', 'SECONDARY', 'SUSPECTED'];

/**
 * The diagnosis set of one encounter, replaced whole. A code is found by name or by code
 * and chosen from the list; a sensitive category says so the moment it is chosen, before
 * anything is saved, because what it changes — who may read this case from now on — is
 * something the person recording it should know while their hand is still on it.
 */
export function DiagnosisEditor({
  caseId,
  encounterId,
  current,
  onDone,
}: {
  caseId: string;
  encounterId: string;
  current: Diagnosis[];
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const [q, setQ] = useState('');
  const search = useIcd10Search(q);
  const put = usePutDiagnoses(caseId, encounterId);
  const [chosen, setChosen] = useState<Chosen[]>(
    current.map((d) => ({
      codeValueId: d.codeValueId,
      code: d.code,
      display: d.display,
      sensitive: d.sensitive,
      diagnosisType: d.diagnosisType,
    })),
  );

  const primaries = chosen.filter((c) => c.diagnosisType === 'PRIMARY').length;
  const anySensitive = chosen.some((c) => c.sensitive);
  const ok = chosen.length > 0 && primaries <= 1;

  function add(value: CodeValue) {
    if (chosen.some((c) => c.codeValueId === value.id)) return;
    setChosen((all) => [
      ...all,
      {
        codeValueId: value.id,
        code: value.code,
        display: value.display,
        sensitive: isSensitiveCode(value),
        diagnosisType: all.some((c) => c.diagnosisType === 'PRIMARY') ? 'SECONDARY' : 'PRIMARY',
      },
    ]);
    setQ('');
  }

  async function save() {
    const items: DiagnosisInput[] = chosen.map((c) => ({
      codeValueId: c.codeValueId,
      diagnosisType: c.diagnosisType,
    }));
    try {
      await put.mutateAsync({ items });
      toast.notify({ tone: 'success', title: t('health.diagnoses.saved') });
      onDone();
    } catch {
      // The problem is rendered below.
    }
  }

  const matches = (search.values.data?.items ?? []).filter((v) => v.active);

  return (
    <div className="grid gap-4" data-testid="diagnosis-editor">
      <FormField label={t('health.diagnoses.search')} hint={t('health.diagnoses.searchHint')}>
        <Input
          name="icd10"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          autoComplete="off"
          spellCheck={false}
        />
      </FormField>
      {search.systemMissing ? (
        <p className="text-fg-muted text-sm">{t('health.diagnoses.noMatch')}</p>
      ) : q.trim().length >= 2 ? (
        <ul className="grid gap-1" data-testid="icd10-matches">
          {search.values.isPending ? (
            <li className="text-fg-muted text-sm">{t('common.loading')}</li>
          ) : matches.length === 0 ? (
            <li className="text-fg-muted text-sm">{t('health.diagnoses.noMatch')}</li>
          ) : (
            matches.map((value) => (
              <li key={value.id}>
                <button
                  type="button"
                  onClick={() => add(value)}
                  className="hover:bg-surface-2 flex w-full items-center gap-3 rounded px-2 py-1.5 text-left text-sm"
                >
                  <span className="font-mono">{value.code}</span>
                  <span className="min-w-0 flex-1 truncate">{value.display}</span>
                  {isSensitiveCode(value) ? (
                    <Badge tone="warning">{t('health.diagnoses.sensitive')}</Badge>
                  ) : null}
                </button>
              </li>
            ))
          )}
        </ul>
      ) : null}

      <div>
        <h4 className="text-sm font-semibold">{t('health.diagnoses.chosen')}</h4>
        {chosen.length === 0 ? (
          <p className="text-fg-muted mt-1 text-sm">{t('health.diagnoses.none')}</p>
        ) : (
          <ul className="mt-2 grid gap-2">
            {chosen.map((c, index) => (
              <li key={c.codeValueId} className="flex flex-wrap items-center gap-2 text-sm">
                <span className="font-mono">{c.code}</span>
                <span className="min-w-0 flex-1">{c.display}</span>
                {c.sensitive ? (
                  <Badge tone="warning">{t('health.diagnoses.sensitive')}</Badge>
                ) : null}
                <Select
                  name={`diagnoses.${index}.type`}
                  aria-label={t('health.diagnoses.type')}
                  value={c.diagnosisType}
                  onChange={(e) =>
                    setChosen((all) =>
                      all.map((x, i) =>
                        i === index ? { ...x, diagnosisType: e.target.value as DiagnosisType } : x,
                      ),
                    )
                  }
                  options={TYPES.map((type) => ({
                    value: type,
                    label: t(`health.diagnoses.types.${type}`),
                  }))}
                />
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setChosen((all) => all.filter((_, i) => i !== index))}
                >
                  {t('health.diagnoses.remove')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>

      {anySensitive ? (
        <p
          role="status"
          className="bg-warning-soft text-fg rounded-md p-3 text-sm"
          data-testid="sensitive-notice"
        >
          {t('health.diagnoses.sensitiveChosen')}
        </p>
      ) : null}
      {primaries > 1 ? (
        <p role="alert" className="text-danger text-sm">
          {t('health.diagnoses.onePrimary')}
        </p>
      ) : null}
      <ProblemAlert problem={put.error ? problemOf(put.error) : null} />
      <div className="flex justify-end gap-2">
        <Button variant="secondary" size="sm" onClick={onDone}>
          {t('health.diagnoses.cancel')}
        </Button>
        <Button size="sm" loading={put.isPending} disabled={!ok} onClick={() => void save()}>
          {t('health.diagnoses.save')}
        </Button>
      </div>
    </div>
  );
}
