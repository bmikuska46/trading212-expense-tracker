"use client";

import { useState, type Ref } from "react";
import { useTranslations } from "next-intl";
import { Combobox, ComboboxContent, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList } from "@/components/ui/combobox";
import { cn } from "@/lib/utils";

export type SelectOption = { value: string; label: string; lang?: string };

type SelectComboboxProps = {
  options: readonly SelectOption[];
  value: string;
  onChange: (value: string) => void;
  onBlur?: () => void;
  ref?: Ref<HTMLInputElement>;
  name?: string;
  id?: string;
  size?: "sm" | "default";
  className?: string;
  disabled?: boolean;
  placeholder?: string;
  lang?: string;
  "aria-invalid"?: boolean;
  "aria-describedby"?: string;
};

// SelectCombobox chooses one option by its value, like a select whose list
// can be filtered by typing. The props match a react-hook-form field, so a
// Controller can spread its field into it.
export function SelectCombobox({ options, value, onChange, onBlur, ref, name, size = "default", className, disabled, ...input }: SelectComboboxProps) {
  const t = useTranslations("common");
  const selected = options.find((option) => option.value === value) ?? null;
  const [open, setOpen] = useState(false);
  // While the input shows the chosen label, focus and presses select it so
  // typing replaces it. A press would otherwise place a caret in the label.
  const showsLabel = (input: HTMLInputElement) => input.value === selected?.label;
  return <Combobox items={options} value={selected} name={name} disabled={disabled} inputRef={ref} autoHighlight onOpenChange={setOpen}
    onValueChange={(option) => { if (option) onChange(option.value); }}
    itemToStringLabel={(option) => option.label} isItemEqualToValue={(item, current) => item.value === current.value}>
    {/* Like a select, Enter never submits the form, so a query that matches
        nothing cannot save it by accident, and Escape on the closed list
        keeps the choice instead of clearing it. */}
    <ComboboxInput className={cn(size === "sm" && "h-7", className)} disabled={disabled} onBlur={onBlur}
      onFocus={(event) => { if (showsLabel(event.currentTarget)) event.currentTarget.select(); }}
      onMouseDown={(event) => {
        if (!showsLabel(event.currentTarget)) return;
        event.preventDefault();
        event.currentTarget.focus();
        event.currentTarget.select();
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter") event.preventDefault();
        if (event.key === "Escape" && !open) event.preventBaseUIHandler();
      }} {...input} />
    <ComboboxContent>
      <ComboboxEmpty>{t("noMatches")}</ComboboxEmpty>
      <ComboboxList>
        {(option: SelectOption) => <ComboboxItem key={option.value} value={option} lang={option.lang}>{option.label}</ComboboxItem>}
      </ComboboxList>
    </ComboboxContent>
  </Combobox>;
}
