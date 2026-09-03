import { forwardRef, type InputHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import { cn } from './cn';

const base =
  'w-full rounded-md border bg-surface-raised px-3 text-sm text-fg placeholder:text-fg-muted ' +
  'disabled:cursor-not-allowed disabled:opacity-60 aria-invalid:border-danger';

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  invalid?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, invalid, ...rest },
  ref,
) {
  return (
    <input
      ref={ref}
      aria-invalid={invalid || rest['aria-invalid'] || undefined}
      className={cn(base, 'h-10 border-line-strong', className)}
      {...rest}
    />
  );
});

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, invalid, ...rest },
  ref,
) {
  return (
    <textarea
      ref={ref}
      aria-invalid={invalid || rest['aria-invalid'] || undefined}
      className={cn(base, 'min-h-24 border-line-strong py-2', className)}
      {...rest}
    />
  );
});
