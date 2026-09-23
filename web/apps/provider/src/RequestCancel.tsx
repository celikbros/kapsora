import {
  randomId,
  type ReasonCommand,
  type ServiceRequest,
  type Versioned,
} from '@kapsora/api-client';
import { usePermission, useTenantId } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Dialog, FormField, ProblemAlert, Select, Textarea, useToast } from '@kapsora/ui';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { problemOf } from './problems';
import { useOps } from './services';

const CANCELLABLE = new Set([
  'DRAFT',
  'SUBMITTED',
  'PENDING_REVIEW',
  'PENDING_DOCUMENT',
  'ELIGIBILITY_FAILED',
]);
const REASONS = ['PROVIDER_WITHDRAWN', 'DUPLICATE_REQUEST', 'INPUT_ERROR'] as const;
type Attempt = { etag: string; key: string; body: ReasonCommand };

/** Withdrawal closes an undecided request. It never cancels an approved authorization. */
export function RequestCancel({ current }: { current: Versioned<ServiceRequest> }) {
  const { t } = useTranslation();
  const allowed = usePermission('service_request.cancel');
  const tenantId = useTenantId();
  const ops = useOps();
  const client = useQueryClient();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [snapshot, setSnapshot] = useState(current);
  const [reason, setReason] = useState('');
  const [text, setText] = useState('');
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const [reloadError, setReloadError] = useState<unknown>(null);
  const [reloading, setReloading] = useState(false);
  const cancel = useMutation({
    mutationFn: (command: Attempt) =>
      ops.requests.cancel(tenantId, current.data.id, command.etag, command.body, command.key),
    onSuccess: (result) => {
      client.setQueryData(['provider', tenantId, 'request', result.data.id], result);
      void client.invalidateQueries({ queryKey: ['provider', tenantId, 'requests'] });
      setOpen(false);
      toast.notify({ tone: 'success', title: t('provider.cancellation.done') });
    },
  });
  if (!allowed || !CANCELLABLE.has(current.data.status)) return null;
  const stale = snapshot.etag !== current.etag;
  const problem = cancel.error ? problemOf(cancel.error) : null;
  const status = problem?.status ?? 0;
  const refused =
    status >= 400 &&
    status < 500 &&
    status !== 408 &&
    status !== 429 &&
    problem?.code !== 'IDEMPOTENCY_IN_PROGRESS';
  const busy = cancel.isPending || reloading;
  const locked = busy || Boolean(attempt) || stale;
  const valid = REASONS.some((value) => value === reason) && text.trim().length <= 1000;
  const canSend = !busy && !refused && (attempt !== null || (!stale && valid));
  async function reload() {
    setReloading(true);
    setReloadError(null);
    try {
      const result = await ops.requests.get(tenantId, current.data.id);
      client.setQueryData(['provider', tenantId, 'request', result.data.id], result);
      setSnapshot(result);
      setAttempt(null);
      cancel.reset();
      // Loading observes the outcome; it never issues another cancellation.
      if (!CANCELLABLE.has(result.data.status)) setOpen(false);
    } catch (error) {
      setReloadError(error);
    } finally {
      setReloading(false);
    }
  }
  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => {
          if (!attempt) {
            setSnapshot(current);
            cancel.reset();
            setReloadError(null);
          }
          setOpen(true);
        }}
      >
        {t('provider.cancellation.action')}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value);
        }}
        title={t('provider.cancellation.action')}
        description={t('provider.cancellation.confirm', { reference: current.data.reference })}
      >
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (!canSend) return;
            const command = attempt ?? {
              etag: snapshot.etag,
              key: randomId(),
              body: {
                reasonCode: reason,
                ...(text.trim() ? { reasonText: text.trim() } : {}),
              },
            };
            setAttempt(command);
            cancel.mutate(command);
          }}
        >
          <ProblemAlert problem={cancel.error ? problemOf(cancel.error) : null} />
          <ProblemAlert problem={reloadError ? problemOf(reloadError) : null} />
          {stale || cancel.error ? (
            <p className="text-fg-muted text-sm">
              {t(
                refused || (stale && !attempt)
                  ? 'provider.cancellation.changed'
                  : 'provider.cancellation.uncertain',
              )}
            </p>
          ) : null}
          <FormField
            label={t('provider.cancellation.reason')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Select
              value={reason}
              disabled={locked}
              onChange={(event) => setReason(event.target.value)}
              placeholder={t('provider.cancellation.choose')}
              options={REASONS.map((code) => ({
                value: code,
                label: t(`provider.cancellation.reasons.${code}`),
              }))}
            />
          </FormField>
          <FormField label={t('requests.reasonText')} hint={t('provider.cancellation.optional')}>
            <Textarea
              rows={3}
              maxLength={1000}
              value={text}
              disabled={locked}
              onChange={(event) => setText(event.target.value)}
            />
          </FormField>
          {stale || cancel.error || reloadError ? (
            <Button
              type="button"
              variant="secondary"
              disabled={busy}
              loading={reloading}
              onClick={() => void reload()}
            >
              {t('provider.correction.reload')}
            </Button>
          ) : null}
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              {t(attempt ? 'common.close' : 'common.cancel')}
            </Button>
            <Button type="submit" variant="danger" loading={cancel.isPending} disabled={!canSend}>
              {t(attempt && !refused ? 'common.retry' : 'provider.cancellation.action')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
