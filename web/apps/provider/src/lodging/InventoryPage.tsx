import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  FormField,
  HelpHint,
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
  useMinWidth,
  useToast,
} from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useInventory, useProperties, usePutInventory, useRoomTypes } from './queries';

/** The first and the day after the last day of a month, as ISO dates. */
function monthRange(month: string): { from: string; to: string } {
  // A month being typed is not a month yet; an empty range keeps the query idle.
  if (!/^\d{4}-\d{2}$/.test(month)) return { from: '', to: '' };
  const [y, m] = month.split('-').map(Number);
  const first = new Date(Date.UTC(y!, m! - 1, 1));
  const next = new Date(Date.UTC(y!, m!, 1));
  return { from: first.toISOString().slice(0, 10), to: next.toISOString().slice(0, 10) };
}

function thisMonth(): string {
  return new Date().toISOString().slice(0, 7);
}

/**
 * Kontenjan: the allotment of one room type, a month at a time, edited in place.
 *
 * `held` and `confirmed` are read-only beside the capacity: they are what the payer's
 * members already have, and a capacity below their sum is refused by the server with the
 * first offending night — the screen shows that refusal as it came and changes nothing.
 * "Sezonu aç" opens a whole range in one call.
 */
export function InventoryPage() {
  const { t } = useTranslation();
  const properties = useProperties();
  const [propertyId, setPropertyId] = useState<string>('');
  const rooms = useRoomTypes(propertyId || null);
  const [roomTypeId, setRoomTypeId] = useState<string>('');
  const [month, setMonth] = useState(thisMonth);
  const range = monthRange(month);
  const inventory = useInventory(roomTypeId || null, range.from, range.to);

  const propertyOptions = (properties.data?.items ?? []).map((p) => ({
    value: p.id,
    label: p.name,
  }));
  const roomOptions = (rooms.data ?? []).map((r) => ({
    value: r.id,
    label: `${r.code} · ${r.name}`,
  }));

  return (
    <div className="grid gap-4">
      <PageHeader title={t('provider.lodging.inventory.title')} />
      <Card>
        <div className="grid gap-3 sm:grid-cols-3">
          <FormField label={t('provider.lodging.property')}>
            <Select
              name="property"
              value={propertyId}
              onChange={(e) => {
                setPropertyId(e.target.value);
                setRoomTypeId('');
              }}
              options={propertyOptions}
              placeholder={t('provider.lodging.choose')}
            />
          </FormField>
          <FormField label={t('provider.lodging.roomType')}>
            <Select
              name="roomType"
              value={roomTypeId}
              onChange={(e) => setRoomTypeId(e.target.value)}
              options={roomOptions}
              placeholder={t('provider.lodging.choose')}
              disabled={!propertyId}
            />
          </FormField>
          <FormField label={t('provider.lodging.inventory.month')}>
            <Input
              type="month"
              name="month"
              value={month}
              onChange={(e) => setMonth(e.target.value)}
            />
          </FormField>
        </div>
      </Card>

      {roomTypeId ? (
        <>
          <SeasonForm roomTypeId={roomTypeId} />
          <Card className="min-w-0">
            <h2 className="text-base font-semibold">{t('provider.lodging.inventory.days')}</h2>
            {inventory.isPending ? (
              <Spinner />
            ) : inventory.isError ? (
              <ProblemAlert problem={problemOf(inventory.error)} />
            ) : (
              <DayTable roomTypeId={roomTypeId} days={inventory.data.days} />
            )}
          </Card>
        </>
      ) : (
        <p className="text-fg-muted text-sm">{t('provider.lodging.inventory.pick')}</p>
      )}
    </div>
  );
}

function SeasonForm({ roomTypeId }: { roomTypeId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const put = usePutInventory(roomTypeId);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [capacity, setCapacity] = useState('');

  function submit(e: FormEvent) {
    e.preventDefault();
    put.mutate(
      { from, to, capacity: Number(capacity) },
      {
        onSuccess: () =>
          toast.notify({ tone: 'success', title: t('provider.lodging.inventory.seasonOpened') }),
      },
    );
  }

  return (
    <Card>
      <h2 className="text-base font-semibold">{t('provider.lodging.inventory.season')}</h2>
      <p className="text-fg-muted mt-1 text-sm">{t('provider.lodging.inventory.seasonHint')}</p>
      <form
        onSubmit={submit}
        className="mt-3 grid gap-3 sm:grid-cols-4"
        noValidate
        data-testid="season-form"
      >
        <FormField
          label={t('provider.lodging.inventory.from')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Input
            type="date"
            name="from"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label={t('provider.lodging.inventory.to')}
          required
          requiredLabel={t('common.requiredMark')}
        >
          <Input
            type="date"
            name="to"
            value={to}
            min={from}
            onChange={(e) => setTo(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label={t('provider.lodging.inventory.capacity')}
          required
          requiredLabel={t('common.requiredMark')}
          help={<HelpHint term="kontenjan" />}
        >
          <Input
            type="number"
            name="capacity"
            inputMode="numeric"
            min={0}
            value={capacity}
            onChange={(e) => setCapacity(e.target.value)}
            required
          />
        </FormField>
        <div className="self-end">
          <Button type="submit" loading={put.isPending} disabled={!from || !to || capacity === ''}>
            {t('provider.lodging.inventory.open')}
          </Button>
        </div>
        <div className="sm:col-span-4">
          <ProblemAlert problem={put.isError ? problemOf(put.error) : null} />
        </div>
      </form>
    </Card>
  );
}

function DayTable({
  roomTypeId,
  days,
}: {
  roomTypeId: string;
  days: {
    stayDate: string;
    allotted: boolean;
    capacity: number;
    held: number;
    confirmed: number;
    available: number;
  }[];
}) {
  const { t } = useTranslation();
  const toast = useToast();
  // One structure per viewport: the table where six columns fit, stacked nights below
  // 768 with the night's one action right after it.
  const wide = useMinWidth(768);
  const put = usePutInventory(roomTypeId);
  const [editing, setEditing] = useState<string | null>(null);
  const [value, setValue] = useState('');

  function save(day: string) {
    put.mutate(
      { from: day, to: day, capacity: Number(value) },
      {
        onSuccess: () => {
          setEditing(null);
          toast.notify({ tone: 'success', title: t('provider.lodging.inventory.daySaved') });
        },
      },
    );
  }

  const controls = (day: { stayDate: string; capacity: number }) =>
    editing === day.stayDate ? (
      <span className="inline-flex gap-1">
        <Button size="sm" onClick={() => save(day.stayDate)} loading={put.isPending}>
          {t('common.save')}
        </Button>
        <Button size="sm" variant="secondary" onClick={() => setEditing(null)}>
          {t('common.cancel')}
        </Button>
      </span>
    ) : (
      <Button
        size="sm"
        variant="secondary"
        onClick={() => {
          setEditing(day.stayDate);
          setValue(String(day.capacity));
        }}
      >
        {t('provider.lodging.inventory.edit')}
      </Button>
    );

  if (!wide) {
    return (
      <div className="mt-3">
        <ProblemAlert problem={put.isError ? problemOf(put.error) : null} className="mb-3" />
        <ul className="grid gap-2" data-testid="inventory-table">
          {days.map((day) => (
            <li
              key={day.stayDate}
              className="bg-surface-raised border-line rounded-lg border p-3"
              data-testid="inventory-row"
            >
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-medium">{formatDate(day.stayDate)}</span>
                {controls(day)}
              </div>
              <dl className="mt-2 grid grid-cols-4 gap-2 text-sm">
                <div>
                  <dt className="text-fg-muted text-xs">
                    {t('provider.lodging.inventory.capacity')}
                  </dt>
                  <dd className="font-mono tabular-nums">
                    {editing === day.stayDate ? (
                      <Input
                        type="number"
                        inputMode="numeric"
                        min={0}
                        value={value}
                        onChange={(e) => setValue(e.target.value)}
                        aria-label={`${t('provider.lodging.inventory.capacity')} ${formatDate(day.stayDate)}`}
                        className="w-20 text-right"
                      />
                    ) : day.allotted ? (
                      day.capacity
                    ) : (
                      <span className="text-fg-muted font-sans text-xs">
                        {t('provider.lodging.inventory.notAllotted')}
                      </span>
                    )}
                  </dd>
                </div>
                <div>
                  <dt className="text-fg-muted text-xs">{t('provider.lodging.inventory.held')}</dt>
                  <dd className="font-mono tabular-nums">{day.held}</dd>
                </div>
                <div>
                  <dt className="text-fg-muted text-xs">
                    {t('provider.lodging.inventory.confirmed')}
                  </dt>
                  <dd className="font-mono tabular-nums">{day.confirmed}</dd>
                </div>
                <div>
                  <dt className="text-fg-muted text-xs">
                    {t('provider.lodging.inventory.available')}
                  </dt>
                  <dd className="font-mono tabular-nums">{day.available}</dd>
                </div>
              </dl>
            </li>
          ))}
        </ul>
      </div>
    );
  }

  return (
    <div className="relative mt-3 overflow-x-auto">
      <ProblemAlert problem={put.isError ? problemOf(put.error) : null} className="mb-3" />
      <Table data-testid="inventory-table">
        <THead>
          <TR>
            <TH>{t('provider.lodging.inventory.date')}</TH>
            <TH className="text-right">{t('provider.lodging.inventory.capacity')}</TH>
            <TH className="text-right">{t('provider.lodging.inventory.held')}</TH>
            <TH className="text-right">{t('provider.lodging.inventory.confirmed')}</TH>
            <TH className="text-right">{t('provider.lodging.inventory.available')}</TH>
            <TH>
              <span className="sr-only">{t('common.actions')}</span>
            </TH>
          </TR>
        </THead>
        <TBody>
          {days.map((day) => (
            <TR key={day.stayDate} data-testid="inventory-row">
              <TD>{formatDate(day.stayDate)}</TD>
              <TD className="text-right font-mono tabular-nums">
                {editing === day.stayDate ? (
                  <Input
                    type="number"
                    inputMode="numeric"
                    min={0}
                    value={value}
                    onChange={(e) => setValue(e.target.value)}
                    aria-label={`${t('provider.lodging.inventory.capacity')} ${formatDate(day.stayDate)}`}
                    className="w-20 text-right"
                  />
                ) : day.allotted ? (
                  day.capacity
                ) : (
                  <span className="text-fg-muted">
                    {t('provider.lodging.inventory.notAllotted')}
                  </span>
                )}
              </TD>
              <TD className="text-right font-mono tabular-nums">{day.held}</TD>
              <TD className="text-right font-mono tabular-nums">{day.confirmed}</TD>
              <TD className="text-right font-mono tabular-nums">{day.available}</TD>
              <TD className="text-right">{controls(day)} </TD>
            </TR>
          ))}
        </TBody>
      </Table>
    </div>
  );
}
