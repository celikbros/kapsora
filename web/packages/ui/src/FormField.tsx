import { Label as RadixLabel } from 'radix-ui';
import { cloneElement, isValidElement, useId, type ReactElement, type ReactNode } from 'react';
import { cn } from './cn';

export interface FormFieldProps {
  label: string;
  /** Rendered as a visible suffix and in aria-required. */
  required?: boolean;
  requiredLabel?: string;
  hint?: string;
  error?: string | undefined;
  /** The control; it receives id, aria-describedby, aria-invalid and aria-required. */
  children: ReactElement<Record<string, unknown>>;
  className?: string;
  /** Extra content after the control, e.g. a remove button. */
  trailing?: ReactNode;
}

/** Label + control + hint + error with the ARIA wiring done once. */
export function FormField({
  label,
  required,
  requiredLabel = 'zorunlu',
  hint,
  error,
  children,
  className,
  trailing,
}: FormFieldProps) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy =
    [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(' ') || undefined;
  const control = isValidElement(children)
    ? cloneElement(children, {
        id,
        'aria-describedby': describedBy,
        'aria-invalid': error ? true : undefined,
        'aria-required': required || undefined,
      })
    : children;

  return (
    <div className={cn('grid gap-1', className)}>
      <RadixLabel.Root htmlFor={id} className="text-fg text-sm font-medium">
        {label}
        {required ? (
          <span className="text-danger ml-1" aria-hidden="true">
            *
          </span>
        ) : null}
        {required ? <span className="sr-only"> ({requiredLabel})</span> : null}
      </RadixLabel.Root>
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">{control}</div>
        {trailing}
      </div>
      {hint ? (
        <p id={hintId} className="text-fg-muted text-xs">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} role="alert" className="text-danger text-xs">
          {error}
        </p>
      ) : null}
    </div>
  );
}
