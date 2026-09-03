import { validateIdentifier, type IdentifierType } from '@kapsora/api-client';
import { useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, Dialog, FormField, Input, ProblemAlert, Select, StepUpDialog } from '@kapsora/ui';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useIdentifierSearch, usePartyCatalogs } from './queries';

export interface IdentifierSearchDialogProps {
  open: boolean;
  onClose: () => void;
}

/**
 * Finds one person by an identifier value. The value stays in this component's state, is
 * cleared when the dialog closes, and never reaches a URL, a query key or storage. The
 * server audits every call and asks for a password re-entry first.
 */
export function IdentifierSearchDialog({ open, onClose }: IdentifierSearchDialogProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const catalogs = usePartyCatalogs();
  const search = useIdentifierSearch();
  const stepUp = useStepUp();
  const [type, setType] = useState('TCKN');
  const [value, setValue] = useState('');
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const sensitiveTypes = (catalogs.data?.identifierTypes ?? []).filter(
    (entry) => entry.status === 'ACTIVE',
  );

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
    const code = validateIdentifier(type as IdentifierType, value);
    if (code) {
      setFieldError(code);
      return;
    }
    setFieldError(null);
    // The step-up wrapper retries this same call once the password is confirmed.
    const found = await stepUp
      .run(() => search.mutateAsync({ type, value }))
      .catch((err: unknown) => {
        setProblem(problemOf(err));
        return undefined;
      });
    if (found) {
      const personId = found.id;
      reset();
      onClose();
      await navigate({ to: '/people/$personId', params: { personId } });
    }
  }

  return (
    <>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!next) close();
        }}
        title={t('people.identifierSearchTitle')}
        description={t('people.identifierSearchIntro')}
      >
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <FormField label={t('people.fields.identifierType')}>
            <Select
              name="type"
              value={type}
              onChange={(e) => setType(e.target.value)}
              options={sensitiveTypes.map((entry) => ({
                value: entry.code,
                label: entry.displayName,
              }))}
            />
          </FormField>
          <FormField
            label={t('people.fields.identifierValue')}
            required
            requiredLabel={t('common.requiredMark')}
            error={fieldError ? fieldErrorMessage(t, fieldError) : undefined}
          >
            <Input
              name="identifier"
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
        action={t('people.identifierSearch')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
