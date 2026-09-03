import type { EntitlementAdjustment } from '@kapsora/api-client';
import { useStepUp } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Dialog,
  FormField,
  Input,
  ProblemAlert,
  StepUpDialog,
  Textarea,
  useToast,
} from '@kapsora/ui';
import { useRef, useState, type FormEvent, type ReactNode } from 'react';

import { useAdjustmentCommands } from '../benefit/queries';
import { problemOf } from '../problems';
import { useEntitlementAccount } from './queries';
import { isNegativeDecimal, signedQuantityText } from './quantity';

/** The two decisions a checker can take on a pending adjustment. */
export type AdjustmentDecision = 'approve' | 'reject';

export interface AdjustmentDialogProps {
  decision: AdjustmentDecision;
  /** The adjustment under decision; the dialog is open while this is set. */
  adjustment: EntitlementAdjustment | null;
  onClose: () => void;
}

function Restated({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1">
      <dt className="text-fg-muted text-xs">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/**
 * Confirms an approval or a rejection. Both restate the account, the delta and the
 * stated reason before anything moves, and both are guarded by a password re-entry: the
 * server refuses them with STEP_UP_REQUIRED and `useStepUp` retries the same call once.
 * The If-Match the row was read with makes a second submit a 412 rather than a second
 * movement, which is what these two commands have instead of an idempotency key.
 */
export function AdjustmentDialog({ decision, adjustment, onClose }: AdjustmentDialogProps) {
  const { t } = useTranslation();
  const toast = useToast();
  const commands = useAdjustmentCommands();
  const stepUp = useStepUp();
  const account = useEntitlementAccount(adjustment?.accountId ?? '');
  const [comment, setComment] = useState('');
  const [reasonCode, setReasonCode] = useState('');
  const [reasonText, setReasonText] = useState('');
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [busy, setBusy] = useState(false);
  // Guards a double click within one tick, before `busy` has reached the button.
  const inFlight = useRef(false);

  const definition = account.data?.data.definition;
  const approving = decision === 'approve';
  const title = t(
    approving ? 'entitlements.adjustments.approve' : 'entitlements.adjustments.reject',
  );

  function close() {
    setComment('');
    setReasonCode('');
    setReasonText('');
    setProblem(null);
    onClose();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!adjustment || inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setProblem(null);
    // The version the row was read with; the server rejects a stale one with 412.
    const etag = `"${adjustment.rowVersion}"`;
    try {
      const decided = await stepUp.run(() =>
        approving
          ? commands.approve.mutateAsync({
              adjustmentId: adjustment.id,
              etag,
              body: comment.trim() ? { comment: comment.trim() } : {},
            })
          : commands.reject.mutateAsync({
              adjustmentId: adjustment.id,
              etag,
              body: {
                reasonCode: reasonCode.trim(),
                ...(reasonText.trim() ? { reasonText: reasonText.trim() } : {}),
              },
            }),
      );
      if (decided) {
        toast.notify({
          tone: 'success',
          title: t(
            approving ? 'entitlements.adjustments.approved' : 'entitlements.adjustments.rejected',
          ),
        });
        close();
      }
    } catch (err) {
      setProblem(problemOf(err));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  return (
    <>
      <Dialog
        open={adjustment !== null}
        onOpenChange={(next) => {
          if (!next && !busy) close();
        }}
        title={title}
        description={definition?.name}
      >
        {adjustment ? (
          <form onSubmit={submit} className="grid gap-4" noValidate>
            <dl className="border-line grid gap-3 rounded-md border p-3 text-sm">
              <Restated label={t('entitlements.adjustments.columns.account')}>
                <span className="font-medium">{definition?.name ?? t('common.loading')}</span>
                {definition ? (
                  <code className="text-fg-muted ml-2 font-mono text-xs">{definition.code}</code>
                ) : null}
              </Restated>
              <Restated label={t('entitlements.adjustments.columns.delta')}>
                <Badge tone={isNegativeDecimal(adjustment.deltaQuantity) ? 'danger' : 'success'}>
                  <span className="font-mono">
                    {signedQuantityText(
                      adjustment.deltaQuantity,
                      definition?.unitType,
                      definition?.currencyCode,
                    )}
                  </span>
                </Badge>
              </Restated>
              <Restated label={t('entitlements.adjustments.columns.reason')}>
                <code className="font-mono text-xs">{adjustment.reasonCode}</code>
                {adjustment.reasonText ? (
                  <span className="text-fg-muted ml-2">{adjustment.reasonText}</span>
                ) : null}
              </Restated>
              <Restated label={t('entitlements.adjustments.columns.requestedBy')}>
                <code className="font-mono text-xs">{adjustment.requestedBy}</code>
                <span className="text-fg-muted ml-2">{formatDateTime(adjustment.requestedAt)}</span>
              </Restated>
            </dl>

            {approving ? (
              <FormField label={t('entitlements.adjustments.fields.comment')}>
                <Textarea
                  name="comment"
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                />
              </FormField>
            ) : (
              <>
                <FormField
                  label={t('entitlements.adjustments.fields.reasonCode')}
                  required
                  requiredLabel={t('common.requiredMark')}
                >
                  <Input
                    name="reasonCode"
                    value={reasonCode}
                    onChange={(e) => setReasonCode(e.target.value)}
                    required
                    autoFocus
                  />
                </FormField>
                <FormField label={t('entitlements.adjustments.fields.reasonText')}>
                  <Textarea
                    name="reasonText"
                    value={reasonText}
                    onChange={(e) => setReasonText(e.target.value)}
                  />
                </FormField>
              </>
            )}

            <ProblemAlert problem={problem} hideFieldErrors />
            {problem?.code === 'MAKER_CHECKER_SAME_ACTOR' ? (
              <p role="status" className="text-fg-muted text-sm">
                {t('entitlements.adjustments.sameActorBlocked')}
              </p>
            ) : null}

            <div className="flex justify-end gap-2">
              <Button variant="secondary" onClick={close} disabled={busy}>
                {t('common.cancel')}
              </Button>
              <Button
                type="submit"
                variant={approving ? 'primary' : 'danger'}
                loading={busy}
                disabled={!approving && reasonCode.trim() === ''}
              >
                {title}
              </Button>
            </div>
          </form>
        ) : null}
      </Dialog>
      <StepUpDialog
        open={stepUp.required}
        action={title}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
