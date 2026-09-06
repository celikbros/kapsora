import type { ClaimLine, NewClaimLine } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { Button, Input, Select, TBody, TD, TH, THead, TR, Table, useMinWidth } from '@kapsora/ui';

import { useServiceDefinitions } from '../queries';

export interface DraftLine {
  serviceDefinitionId: string;
  unitType: string;
  quantity: string;
  lineAmount: string;
  description: string;
}

export function draftFrom(lines: ClaimLine[]): DraftLine[] {
  return lines.map((l) => ({
    serviceDefinitionId: l.serviceDefinitionId,
    unitType: l.unitType,
    quantity: l.quantity,
    lineAmount: l.lineAmount,
    description: l.description ?? '',
  }));
}

export function emptyLine(): DraftLine {
  return {
    serviceDefinitionId: '',
    unitType: 'SESSION',
    quantity: '1',
    lineAmount: '',
    description: '',
  };
}

export function toNewLines(lines: DraftLine[]): NewClaimLine[] {
  return lines.map((l, index) => ({
    lineNo: index + 1,
    serviceDefinitionId: l.serviceDefinitionId,
    unitType: l.unitType,
    quantity: l.quantity.trim(),
    lineAmount: l.lineAmount.trim(),
    currencyCode: 'TRY',
    description: l.description.trim() ? l.description.trim() : null,
  }));
}

export function linesValid(lines: DraftLine[]): boolean {
  return (
    lines.length > 0 &&
    lines.every(
      (l) => l.serviceDefinitionId !== '' && l.quantity.trim() !== '' && l.lineAmount.trim() !== '',
    )
  );
}

/**
 * The claim's lines, edited in place: the service, what was done, how many, and what the
 * provider asks for it — in the same column order the decided table reads in, so a draft
 * beside a decided version lines up. No price is computed or shown here — the contract
 * price, the plan's share and the member's share are the server's answer on submit, and a
 * draft that showed a figure would be showing a guess. From `md` up it is a table; below,
 * one block per line, every field labelled.
 */
export function LinesEditor({
  lines,
  onChange,
}: {
  lines: DraftLine[];
  onChange: (lines: DraftLine[]) => void;
}) {
  const { t } = useTranslation();
  const options = useServiceDefinitions();
  const wide = useMinWidth(768);
  const update = (index: number, patch: Partial<DraftLine>) =>
    onChange(lines.map((l, i) => (i === index ? { ...l, ...patch } : l)));
  const pickService = (index: number, id: string) => {
    const picked = options.data?.find((o) => o.value === id);
    update(index, {
      serviceDefinitionId: id,
      ...(picked?.unitType ? { unitType: picked.unitType } : {}),
    });
  };
  const serviceOptions = (options.data ?? []).map((o) => ({ value: o.value, label: o.label }));

  const controls = (line: DraftLine, index: number) => ({
    service: (
      <Select
        name={`lines.${index}.serviceDefinitionId`}
        aria-label={t('claims.lines.service')}
        value={line.serviceDefinitionId}
        onChange={(e) => pickService(index, e.target.value)}
        placeholder={t('common.none')}
        options={serviceOptions}
      />
    ),
    description: (
      <Input
        name={`lines.${index}.description`}
        aria-label={t('claims.lines.description')}
        value={line.description}
        onChange={(e) => update(index, { description: e.target.value })}
      />
    ),
    quantity: (
      <Input
        name={`lines.${index}.quantity`}
        aria-label={t('claims.lines.quantity')}
        value={line.quantity}
        onChange={(e) => update(index, { quantity: e.target.value })}
        inputMode="decimal"
        className="w-24 text-right font-mono"
      />
    ),
    asked: (
      <Input
        name={`lines.${index}.lineAmount`}
        aria-label={t('claims.lines.asked')}
        value={line.lineAmount}
        onChange={(e) => update(index, { lineAmount: e.target.value })}
        inputMode="decimal"
        className="w-32 text-right font-mono"
      />
    ),
    remove: (
      <Button
        size="sm"
        variant="ghost"
        onClick={() => onChange(lines.filter((_, i) => i !== index))}
      >
        {t('claims.lines.remove')}
      </Button>
    ),
  });

  return (
    <div className="grid gap-3">
      {lines.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('claims.lines.empty')}</p>
      ) : wide ? (
        <Table data-testid="claim-lines-editor">
          <THead>
            <TR>
              <TH>{t('claims.lines.line')}</TH>
              <TH>{t('claims.lines.service')}</TH>
              <TH>{t('claims.lines.description')}</TH>
              <TH className="text-right">{t('claims.lines.quantity')}</TH>
              <TH className="text-right">{t('claims.lines.asked')}</TH>
              <TH>
                <span className="sr-only">{t('common.actions')}</span>
              </TH>
            </TR>
          </THead>
          <TBody>
            {lines.map((line, index) => {
              const c = controls(line, index);
              return (
                <TR key={index}>
                  <TD className="font-mono">{index + 1}</TD>
                  <TD>{c.service}</TD>
                  <TD>{c.description}</TD>
                  <TD>
                    <div className="flex items-center justify-end gap-2">
                      {c.quantity}
                      <span className="text-fg-muted text-xs">{t(`units.${line.unitType}`)}</span>
                    </div>
                  </TD>
                  <TD>{c.asked}</TD>
                  <TD>{c.remove}</TD>
                </TR>
              );
            })}
          </TBody>
        </Table>
      ) : (
        <ol className="grid gap-3" data-testid="claim-lines-editor">
          {lines.map((line, index) => {
            const c = controls(line, index);
            return (
              <li key={index} className="border-line grid gap-2 rounded-md border p-3">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-sm">{index + 1}</span>
                  {c.remove}
                </div>
                <label className="grid gap-1 text-xs">
                  <span className="text-fg-muted">{t('claims.lines.service')}</span>
                  {c.service}
                </label>
                <label className="grid gap-1 text-xs">
                  <span className="text-fg-muted">{t('claims.lines.description')}</span>
                  {c.description}
                </label>
                <div className="grid grid-cols-2 gap-2">
                  <label className="grid gap-1 text-xs">
                    <span className="text-fg-muted">
                      {t('claims.lines.quantity')} · {t(`units.${line.unitType}`)}
                    </span>
                    {c.quantity}
                  </label>
                  <label className="grid gap-1 text-xs">
                    <span className="text-fg-muted">{t('claims.lines.asked')}</span>
                    {c.asked}
                  </label>
                </div>
              </li>
            );
          })}
        </ol>
      )}
      <p className="text-fg-muted text-sm">{t('claims.lines.askedHint')}</p>
      <div>
        <Button variant="secondary" size="sm" onClick={() => onChange([...lines, emptyLine()])}>
          {t('claims.lines.add')}
        </Button>
      </div>
    </div>
  );
}
