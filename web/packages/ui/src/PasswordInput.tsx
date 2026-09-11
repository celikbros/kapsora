import { useTranslation } from '@kapsora/i18n';
import { forwardRef, useState } from 'react';

import { cn } from './cn';
import { Input, type InputProps } from './Input';

export type PasswordInputProps = Omit<InputProps, 'type'>;

/** The eye, open when the password is hidden (the action shows it), struck through when shown. */
function EyeIcon({ struck }: { struck: boolean }) {
  return (
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        d="M2.5 10s2.8-5.5 7.5-5.5 7.5 5.5 7.5 5.5-2.8 5.5-7.5 5.5S2.5 10 2.5 10Z"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinejoin="round"
      />
      <circle cx="10" cy="10" r="2.5" stroke="currentColor" strokeWidth="1.75" />
      {struck ? (
        <path
          d="M3.5 3.5 16.5 16.5"
          stroke="currentColor"
          strokeWidth="1.75"
          strokeLinecap="round"
        />
      ) : null}
    </svg>
  );
}

/**
 * A password field with a show/hide control at its end. The control is a real button whose
 * state is in aria-pressed. Its name is the verb alone — "Göster", "Gizle" — because the field's
 * label already says what is shown, and a second control named "Parola…" beside the field would
 * be two things answering to the same name. Showing only changes the input's type: the value
 * stays in the input and goes nowhere else.
 */
export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  function PasswordInput({ className, disabled, ...rest }, ref) {
    const { t } = useTranslation();
    const [visible, setVisible] = useState(false);
    const label = visible
      ? t('auth.hidePassword', { defaultValue: 'Gizle' })
      : t('auth.showPassword', { defaultValue: 'Göster' });
    return (
      <div className="relative">
        <Input
          ref={ref}
          {...rest}
          {...(disabled !== undefined ? { disabled } : {})}
          type={visible ? 'text' : 'password'}
          className={cn('pr-11', className)}
        />
        <button
          type="button"
          onClick={() => setVisible((v) => !v)}
          aria-pressed={visible}
          aria-label={label}
          title={label}
          {...(typeof rest.id === 'string' ? { 'aria-controls': rest.id } : {})}
          {...(disabled !== undefined ? { disabled } : {})}
          data-testid="password-toggle"
          className="text-fg-muted hover:text-fg focus-visible:outline-primary absolute inset-y-0 right-0 flex w-10 items-center justify-center rounded-r-md focus-visible:outline-2 focus-visible:-outline-offset-2 disabled:cursor-not-allowed disabled:opacity-60"
        >
          <EyeIcon struck={visible} />
        </button>
      </div>
    );
  },
);
