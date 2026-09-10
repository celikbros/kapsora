import { validateIdentifier, type IdentifierType, type Problem } from '@kapsora/api-client';
import { useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, Dialog, FormField, Input, ProblemAlert, Select, StepUpDialog } from '@kapsora/ui';
import { useState, type FormEvent } from 'react';

import { useIdentifierSearch, usePeopleByName } from './queries';
import { problemOf } from './problems';

export interface PickedMember {
  id: string;
  displayName: string;
}

/**
 * The member, by name — or by identifier behind a password. The identifier value lives in
 * this component's state, is cleared when the dialog closes, and reaches no URL, query key
 * or storage; the server audits the call and asks for the password first.
 */
export function MemberPicker({
  member,
  onPick,
}: {
  member: PickedMember | null;
  onPick: (member: PickedMember | null) => void;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [dialog, setDialog] = useState(false);
  const [type, setType] = useState('TCKN');
  const [value, setValue] = useState('');
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [problem, setProblem] = useState<Problem | null>(null);
  const people = usePeopleByName(query);
  const search = useIdentifierSearch();
  const stepUp = useStepUp();

  function closeDialog() {
    setValue('');
    setFieldError(null);
    setProblem(null);
    search.reset();
    setDialog(false);
  }

  async function submitIdentifier(event: FormEvent) {
    event.preventDefault();
    const code = validateIdentifier(type as IdentifierType, value);
    if (code) {
      setFieldError(code);
      return;
    }
    setFieldError(null);
    setProblem(null);
    const found = await stepUp
      .run(() => search.mutateAsync({ type, value }))
      .catch((err: unknown) => {
        setProblem(problemOf(err));
        return undefined;
      });
    if (found) {
      onPick({ id: found.id, displayName: found.displayName });
      closeDialog();
    }
  }

  return (
    <div className="grid gap-3">
      <FormField
        label={t('provider.newRequest.member')}
        required
        requiredLabel={t('common.requiredMark')}
        hint={t('provider.newRequest.memberSearchHint')}
      >
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            name="memberSearch"
            aria-label={t('provider.newRequest.memberSearch')}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              if (member) onPick(null);
            }}
            autoComplete="off"
            className="flex-1"
          />
          <Button type="button" variant="secondary" onClick={() => setDialog(true)}>
            {t('provider.newRequest.byIdentifier')}
          </Button>
        </div>
      </FormField>
      {member ? (
        <p role="status" className="bg-primary-soft rounded-md px-3 py-2 text-sm font-medium">
          {t('provider.newRequest.found', { name: member.displayName })}
        </p>
      ) : people.data && people.data.items.length > 0 ? (
        <ul
          data-testid="member-candidates"
          className="border-line divide-border max-h-56 divide-y overflow-auto rounded-md border text-sm"
        >
          {people.data.items.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                className="hover:bg-surface-2 w-full px-3 py-2 text-left"
                onClick={() => {
                  onPick({ id: p.id, displayName: p.displayName });
                  setQuery(p.displayName);
                }}
              >
                {p.displayName}
              </button>
            </li>
          ))}
        </ul>
      ) : null}

      <Dialog
        open={dialog}
        onOpenChange={(next) => {
          if (!next) closeDialog();
        }}
        title={t('provider.newRequest.byIdentifier')}
        description={t('provider.newRequest.identifierHint')}
      >
        <form onSubmit={submitIdentifier} className="grid gap-4" noValidate>
          <FormField label={t('people.fields.identifierType')}>
            <Select
              name="type"
              value={type}
              onChange={(e) => setType(e.target.value)}
              options={[
                { value: 'TCKN', label: 'TCKN' },
                { value: 'PASSPORT', label: t('people.identifierTypes.PASSPORT', 'Pasaport') },
              ]}
            />
          </FormField>
          <FormField
            label={t('provider.newRequest.identifier')}
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
              autoFocus
            />
          </FormField>
          <ProblemAlert problem={problem} />
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="secondary"
              onClick={closeDialog}
              disabled={search.isPending}
            >
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
        action={t('provider.newRequest.byIdentifier')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </div>
  );
}
