import type {
  HTMLAttributes,
  PropsWithChildren,
  ReactNode,
  TableHTMLAttributes,
  TdHTMLAttributes,
  ThHTMLAttributes,
} from 'react';
import styles from './Table.module.scss';

interface TableProps extends TableHTMLAttributes<HTMLTableElement> {
  className?: string;
  cols?: ReactNode;
}

export function Table({ children, cols, className, ...rest }: PropsWithChildren<TableProps>) {
  const tableCls = [styles.table, className].filter(Boolean).join(' ');
  return (
    <div className={styles.wrap}>
      <div className={styles.scroll}>
        <table className={tableCls} {...rest}>
          {cols ? <colgroup>{cols}</colgroup> : null}
          {children}
        </table>
      </div>
    </div>
  );
}

export function TableHeader({
  children,
  className,
  ...rest
}: PropsWithChildren<HTMLAttributes<HTMLTableSectionElement>>) {
  return (
    <thead className={[styles.head, className].filter(Boolean).join(' ')} {...rest}>
      {children}
    </thead>
  );
}

export function TableBody({
  children,
  className,
  ...rest
}: PropsWithChildren<HTMLAttributes<HTMLTableSectionElement>>) {
  return (
    <tbody className={[styles.body, className].filter(Boolean).join(' ')} {...rest}>
      {children}
    </tbody>
  );
}

interface TableRowProps extends HTMLAttributes<HTMLTableRowElement> {
  selected?: boolean;
}

export function TableRow({
  children,
  className,
  selected,
  ...rest
}: PropsWithChildren<TableRowProps>) {
  const cls = [styles.row, selected ? styles.selected : null, className].filter(Boolean).join(' ');
  return (
    <tr className={cls} {...rest}>
      {children}
    </tr>
  );
}

interface TableHeadProps extends ThHTMLAttributes<HTMLTableCellElement> {
  alignRight?: boolean;
}

export function TableHead({
  children,
  className,
  alignRight,
  ...rest
}: PropsWithChildren<TableHeadProps>) {
  const cls = [alignRight ? styles.alignRight : null, className].filter(Boolean).join(' ');
  return (
    <th className={cls || undefined} {...rest}>
      {children}
    </th>
  );
}

interface TableCellProps extends TdHTMLAttributes<HTMLTableCellElement> {
  alignRight?: boolean;
}

export function TableCell({
  children,
  className,
  alignRight,
  ...rest
}: PropsWithChildren<TableCellProps>) {
  const cls = [alignRight ? styles.alignRight : null, className].filter(Boolean).join(' ');
  return (
    <td className={cls || undefined} {...rest}>
      {children}
    </td>
  );
}
