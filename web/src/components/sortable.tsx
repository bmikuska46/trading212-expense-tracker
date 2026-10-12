"use client";

import { useState, type ComponentProps, type MouseEvent, type ReactNode } from "react";
import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react";
import { headClass } from "@/components/panel";
import { TableHead } from "@/components/ui/table";
import { collator as localeCollator } from "@/lib/format";
import { cn } from "@/lib/utils";

export type Sort<K extends string = string> = { key: K; desc: boolean };
type Direction = "asc" | "desc" | false;

const collator = localeCollator({ numeric: true, sensitivity: "base" });

// SortButton is a column label that toggles the sort. It sits inside a table
// head, which carries aria-sort.
export function SortButton({ direction, onClick, children }: { direction: Direction; onClick?: (event: MouseEvent) => void; children: ReactNode }) {
  return <button type="button" onClick={onClick}
    className={cn("inline-flex items-center gap-1 rounded-sm uppercase outline-none hover:text-foreground focus-visible:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
      direction && "text-muted-foreground")}>
    {children}
    {direction === "asc" ? <ArrowUp className="size-3" aria-hidden="true" /> : direction === "desc"
      ? <ArrowDown className="size-3" aria-hidden="true" /> : <ArrowUpDown className="size-3 opacity-40" aria-hidden="true" />}
  </button>;
}

export const ariaSort = (direction: Direction) => direction === "asc" ? "ascending" : direction === "desc" ? "descending" : undefined;

// SortHead is a panel table head whose label sorts the table by key.
export function SortHead<K extends string>({ sort, sortKey, onSort, className, children, ...props }: {
  sort: Sort<K>; sortKey: K; onSort: (key: K) => void; children: ReactNode;
} & Omit<ComponentProps<typeof TableHead>, "onClick">) {
  const direction: Direction = sort.key === sortKey && (sort.desc ? "desc" : "asc");
  return <TableHead scope="col" className={cn(headClass, className)} aria-sort={ariaSort(direction)} {...props}>
    <SortButton direction={direction} onClick={() => onSort(sortKey)}>{children}</SortButton>
  </TableHead>;
}

// nextSort flips the direction of the current key. A new key starts with the
// largest numbers or the start of the alphabet.
export function nextSort<K extends string>(sort: Sort<K>, key: K, numeric: boolean): Sort<K> {
  return sort.key === key ? { key, desc: !sort.desc } : { key, desc: numeric };
}

type SortValue = string | number | null | undefined;

// useSort sorts rows in the browser. Each key reads one comparable value from a
// row; missing values stay last in both directions.
export function useSort<T, K extends string>(rows: T[], keys: Record<K, (row: T) => SortValue>, initial: Sort<NoInfer<K>>) {
  const [sort, setSort] = useState(initial);
  const read = keys[sort.key];
  const sorted = rows.toSorted((a, b) => {
    const left = read(a), right = read(b);
    if (left == null || right == null) return left == null ? (right == null ? 0 : 1) : -1;
    const order = typeof left === "number" && typeof right === "number" ? left - right : collator.compare(String(left), String(right));
    return sort.desc ? -order : order;
  });
  const toggle = (key: K) => setSort(nextSort(sort, key, rows.some((row) => typeof keys[key](row) === "number")));
  return { rows: sorted, sort, toggle };
}
