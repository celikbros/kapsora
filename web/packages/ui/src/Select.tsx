import { forwardRef, type SelectHTMLAttributes } from 'react';
import { cn } from './cn';

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'children'> {
  options: SelectOption[];
  /** Placeholder row rendered as an empty-value option. */
  placeholder?: string;
  invalid?: boolean;
}

/**
 * Native select styled with the tokens. Native controls give the best keyboard and
 * screen-reader behaviour in forms; Radix Select is reserved for rich pickers later.
 */
export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { options, placeholder, invalid, className, ...rest },
  ref,
) {
  return (
    <select
      ref={ref}
      aria-invalid={invalid || rest['aria-invalid'] || undefined}
      className={cn(
        'h-10 w-full rounded-md border border-line-strong bg-surface-raised px-3 text-sm text-fg',
        'disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger',
        className,
      )}
      {...rest}
    >
      {placeholder !== undefined ? <option value="">{placeholder}</option> : null}
      {options.map((o) => (
        <option key={o.value} value={o.value} disabled={o.disabled}>
          {o.label}
        </option>
      ))}
    </select>
  );
});
