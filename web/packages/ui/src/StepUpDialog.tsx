import { useTranslation } from '@kapsora/i18n';
import { useState, type FormEvent } from 'react';

import { Button } from './Button';
import { Dialog } from './Dialog';
import { FormField } from './FormField';
import { PasswordInput } from './PasswordInput';
import { ProblemAlert, type ProblemLike } from './ProblemAlert';

export interface StepUpDialogProps {
  open: boolean;
  /** What the operator is about to do, shown so the password prompt is not a surprise. */
  action: string;
  busy: boolean;
  problem: ProblemLike | null;
  onConfirm: (password: string) => void;
  onCancel: () => void;
}

/**
 * Asks for the password again before a sensitive action. The value lives in this
 * component's state for the length of the submit and is cleared immediately after; it is
 * never lifted into a store, a query key or browser storage.
 */
export function StepUpDialog({
  open,
  action,
  busy,
  problem,
  onConfirm,
  onCancel,
}: StepUpDialogProps) {
  const { t } = useTranslation();
  const [password, setPassword] = useState('');

  function submit(event: FormEvent) {
    event.preventDefault();
    onConfirm(password);
    setPassword('');
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setPassword('');
          onCancel();
        }
      }}
      title={t('auth.stepUpTitle')}
      description={t('auth.stepUpIntro', { action })}
    >
      <form onSubmit={submit} className="grid gap-4" noValidate>
        <FormField label={t('auth.password')} required requiredLabel={t('common.requiredMark')}>
          <PasswordInput
            name="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoFocus
          />
        </FormField>
        <ProblemAlert problem={problem} />
        <div className="flex justify-end gap-2">
          <Button
            variant="secondary"
            onClick={() => {
              setPassword('');
              onCancel();
            }}
            disabled={busy}
          >
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={busy} disabled={password === ''}>
            {t('auth.stepUpConfirm')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
