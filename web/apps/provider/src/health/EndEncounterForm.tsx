import { etagOf, randomId, type Encounter } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { Button, FormField, Input, ProblemAlert, useToast } from '@kapsora/ui';
import { useRef, useState, type FormEvent } from 'react';
import { problemOf } from '../problems';
import { useOps } from '../services';
import { useCase, useEndEncounter } from './queries';
import { nowLocal, toInstant } from './words';

type Attempt = { etag: string; key: string; body: { endedAt: string } };

/** An uncertain end retries its original time/key/version; reload only observes. */
export function EndEncounterForm({
  encounter,
  onDone,
}: {
  encounter: Encounter;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const tenantId = useTenantId();
  const ops = useOps();
  const toast = useToast();
  const query = useCase(encounter.caseId);
  const end = useEndEncounter(encounter.caseId, encounter.id);
  const [snapshot, setSnapshot] = useState(encounter);
  const [endedAt, setEndedAt] = useState(nowLocal());
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const [reloading, setReloading] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  const inFlight = useRef(false);
  const problem = end.error ? problemOf(end.error) : null;
  const status = problem?.status ?? 0;
  const refused =
    status >= 400 &&
    status < 500 &&
    status !== 408 &&
    status !== 429 &&
    problem?.code !== 'IDEMPOTENCY_IN_PROGRESS';
  const busy = end.isPending || reloading;
  const stale = snapshot.rowVersion !== encounter.rowVersion;
  const valid =
    Boolean(endedAt) &&
    Number.isFinite(Date.parse(endedAt)) &&
    Date.parse(endedAt) >= Date.parse(snapshot.startedAt);
  const locked = busy || Boolean(attempt) || stale;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || busy || refused || (!attempt && (!valid || stale))) return;
    inFlight.current = true;
    const command = attempt ?? {
      etag: etagOf(snapshot.rowVersion),
      key: randomId(),
      body: { endedAt: toInstant(endedAt) },
    };
    setAttempt(command);
    try {
      await end.mutateAsync(command);
      toast.notify({ tone: 'success', title: t('health.encounters.end.done') });
      onDone();
    } catch {
      // Preserve the exact attempt until replay or an explicit read resolves it.
    } finally {
      inFlight.current = false;
    }
  }
  async function reload() {
    if (inFlight.current || busy) return;
    setReloading(true);
    setReloadError(null);
    try {
      const current = await ops.health.getEncounter(tenantId, encounter.id);
      await query.refetch();
      setSnapshot(current.data);
      setAttempt(null);
      end.reset();
      if (current.data.endedAt) onDone();
    } catch (error) {
      setReloadError(error);
    } finally {
      setReloading(false);
    }
  }

  return (
    <form
      onSubmit={(event) => void submit(event)}
      className="grid gap-3 py-3"
      data-testid="end-encounter-form"
    >
      <p className="text-sm text-fg-muted">
        {t('health.encounters.end.hint', { started: formatDateTime(snapshot.startedAt) })}
      </p>
      <FormField
        label={t('health.encounters.form.endedAt')}
        required
        requiredLabel={t('common.requiredMark')}
        error={!valid && endedAt ? t('health.encounters.end.invalid') : undefined}
      >
        <Input
          name="endedAt"
          type="datetime-local"
          value={endedAt}
          disabled={locked}
          onChange={(event) => setEndedAt(event.target.value)}
          className="max-w-sm"
        />
      </FormField>
      <ProblemAlert problem={reloadError ? problemOf(reloadError) : problem} />
      {attempt && end.isError ? (
        <p role="status" className="text-sm text-fg-muted">
          {t(refused ? 'health.encounters.end.reload' : 'health.encounters.end.uncertain')}
        </p>
      ) : null}
      {stale ? (
        <p role="status" className="text-sm text-fg-muted">
          {t('health.encounters.end.reload')}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button
          type="submit"
          size="sm"
          loading={end.isPending}
          disabled={busy || refused || (!attempt && (!valid || stale))}
        >
          {t(attempt ? 'common.retry' : 'health.encounters.end.action')}
        </Button>
        {attempt || stale ? (
          <Button
            size="sm"
            variant="secondary"
            loading={reloading}
            disabled={busy}
            onClick={() => void reload()}
          >
            {t('health.encounters.end.refresh')}
          </Button>
        ) : null}
        <Button size="sm" variant="ghost" disabled={busy || Boolean(attempt)} onClick={onDone}>
          {t('common.cancel')}
        </Button>
      </div>
    </form>
  );
}
