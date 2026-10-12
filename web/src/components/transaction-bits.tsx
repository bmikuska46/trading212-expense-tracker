import { useTranslations } from "next-intl";
import type { Transaction } from "@/lib/api";
import { dateFormat, formatEURCents, formatMoney } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// These small pieces render a transaction the same way on the dashboard and
// in the register, without pulling the register table into the dashboard.

const dayMonth = dateFormat({ month: "short", day: "2-digit", timeZone: "UTC" });

export const shortDate = (date: string) => dayMonth.format(new Date(`${date}T00:00:00Z`));

export type TransactionsT = ReturnType<typeof useTranslations<"transactions">>;

export function payeeText(t: TransactionsT, row: Transaction): string {
  if (row.kind === "transfer") {
    if (!row.transferAccountName) return t("bits.transfer");
    return t(row.amount.startsWith("-") ? "bits.toAccount" : "bits.fromAccount", { account: row.transferAccountName });
  }
  return row.payeeName || t("bits.noPayee");
}

// payeeInitial is the letter that stands in for the payee or, on transfers,
// the other account.
export function payeeInitial(t: TransactionsT, row: Transaction): string {
  const name = row.kind === "transfer" ? row.transferAccountName : row.payeeName;
  return (name || payeeText(t, row)).charAt(0).toUpperCase();
}

export function categoryText(t: TransactionsT, row: Transaction): string {
  if (row.kind === "transfer") return t("bits.transfer");
  return row.splitCount > 0 ? t("bits.split", { count: row.splitCount }) : row.categoryName;
}

// CategoryChip outlines the category. Transfers have none because the totals
// leave them out, and a dashed outline marks rows that still need one.
export function CategoryChip({ row }: { row: Transaction }) {
  const t = useTranslations("transactions");
  if (row.kind === "transfer") return <span className="text-xs text-faint">{t("bits.notCounted")}</span>;
  const missing = row.splitCount === 0 && row.categoryName === "Uncategorized";
  return <span className={cn("rounded-[5px] border border-input px-1.5 py-px text-xs whitespace-nowrap",
    missing ? "border-dashed text-faint" : "text-muted-foreground")}>{categoryText(t, row)}</span>;
}

// Amount shows the signed source amount. Money coming in is blue, transfers
// are muted because the totals leave them out, and foreign amounts show the
// EUR value the totals use beneath them.
export function Amount({ row, className }: { row: Transaction; className?: string }) {
  return <span className={cn("block text-right font-mono font-medium whitespace-nowrap", className)}>
    <span className={cn(row.kind === "transfer" ? "text-muted-foreground" : row.eurCents > 0 && "text-income-text")}>
      {formatMoney(row.amount, row.currency, { signed: true })}
    </span>
    {row.currency !== "EUR" && <span className="block text-[0.6875rem] font-normal text-faint">{formatEURCents(row.eurCents, { signed: true })}</span>}
  </span>;
}

export function TransactionBadges({ row, imported = false }: { row: Transaction; imported?: boolean }) {
  const t = useTranslations("transactions.bits.badges");
  return <>
    {row.reviewStatus === "needs_review" && <Badge variant="warning">{t("review")}</Badge>}
    {row.reviewStatus === "source_conflict" && <Badge variant="destructive">{t("sourceChanged")}</Badge>}
    {row.kind === "refund" && <Badge variant="secondary">{row.refundOf ? t("linkedRefund") : t("refund")}</Badge>}
    {row.kind === "transfer" && <Badge variant="secondary">{t("transfer")}</Badge>}
    {imported && row.imported && <Badge variant="outline">{t("imported")}</Badge>}
  </>;
}
