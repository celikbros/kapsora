import type { Practitioner, RegistrationAuthority } from '@kapsora/api-client';
import { useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, Dialog, FormField, Input, ProblemAlert, Select, StepUpDialog } from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useRegistrationSearch } from './queries';
import { REGISTRATION_AUTHORITIES, registrationSearchSchema, parseIssueMessage } from './schema';

export interface RegistrationSearchDialogProps {
  open: boolean;
  onClose: () => void;
  /** Handed the practitioner the server found, so the caller can go to them. */
  onFound: (practitioner: Practitioner) => void;
}

/**
 * Finds one practitioner by their registration number. The number stays in this
 * component's state, is cleared when the dialog closes, and never reaches a URL, a query
 * key or storage — it travels in the request body only. The server audits every call and
 * asks for the password again first.
 */
export function RegistrationSearchDialog({
  open,
  onClose,
  onFound,
}: RegistrationSearchDialogProps) {
  const { t } = useTranslation();
  const search = useRegistrationSearch();
  const stepUp = useStepUp();
  const [authority, setAuthority] = useState<RegistrationAuthority>('TTB');
  const [value, setValue] = useState('');
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  function message(issueMessage: string): string {
    const { code, params } = parseIssueMessage(issueMessage);
    return fieldErrorMessage(t, code, undefined, params);
  }

  function reset() {
    setValue('');
    setFieldError(null);
    setProblem(null);
    search.reset();
  }

  function close() {
    reset();
    onClose();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setProblem(null);
    const parsed = registrationSearchSchema.safeParse({
      registrationAuthority: authority,
      registrationNumber: value,
    });
    if (!parsed.success) {
      const issue = parsed.error.issues[0];
      setFieldError(issue ? issue.message : 'REQUIRED');
      return;
    }
    setFieldError(null);
    // The step-up wrapper retries this same call once the password is confirmed, and
    // hands back what the retry returned.
    const found = await stepUp
      .run(() => search.mutateAsync(parsed.data))
      .catch((err: unknown) => {
        setProblem(problemOf(err));
        return undefined;
      });
    if (found) {
      reset();
      onClose();
      onFound(found);
    }
  }

  return (
    <>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!next) close();
        }}
        title={t('practitioners.searchTitle')}
        description={t('practitioners.searchIntro')}
      >
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <FormField label={t('practitioners.fields.registrationAuthority')}>
            <Select
              name="registrationAuthority"
              value={authority}
              onChange={(e) => setAuthority(e.target.value as RegistrationAuthority)}
              options={REGISTRATION_AUTHORITIES.map((entry) => ({
                value: entry,
                label: t(`practitioners.authorities.${entry}`),
              }))}
            />
          </FormField>
          <FormField
            label={t('practitioners.fields.registrationNumber')}
            required
            requiredLabel={t('common.requiredMark')}
            error={fieldError ? message(fieldError) : undefined}
          >
            <Input
              name="registrationNumber"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              inputMode="numeric"
              autoComplete="off"
              required
              autoFocus
            />
          </FormField>
          <ProblemAlert problem={problem} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={close} disabled={search.isPending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={search.isPending} disabled={value.trim() === ''}>
              {t('common.search')}
            </Button>
          </div>
        </form>
      </Dialog>
      <StepUpDialog
        open={stepUp.required}
        action={t('practitioners.search')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
