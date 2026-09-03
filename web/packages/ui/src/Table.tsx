import type {
  HTMLAttributes,
  TableHTMLAttributes,
  TdHTMLAttributes,
  ThHTMLAttributes,
} from 'react';
import { cn } from './cn';

/** Table primitives; data wiring (TanStack Table) stays in the app. */
export function Table({ className, ...rest }: TableHTMLAttributes<HTMLTableElement>) {
  return (
    <div className="border-line overflow-x-auto rounded-md border">
      <table className={cn('k-table', className)} {...rest} />
    </div>
  );
}

export function THead(props: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead {...props} />;
}

export function TBody(props: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody {...props} />;
}

export function TR({
  clickable,
  ...rest
}: HTMLAttributes<HTMLTableRowElement> & { clickable?: boolean }) {
  return (
    <tr
      data-clickable={clickable ? 'true' : undefined}
      tabIndex={clickable ? 0 : undefined}
      {...rest}
    />
  );
}

export function TH(props: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th scope="col" {...props} />;
}

export function TD(props: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td {...props} />;
}
