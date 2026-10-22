"use client";

import { useMemo, type ComponentProps } from "react";
import { timezones } from "@/lib/format";
import { SelectCombobox } from "@/components/select-combobox";

// TimezoneSelect lists the IANA zones the browser knows. The server validates
// the choice against its own time zone database.
export function TimezoneSelect({ current, ...props }: Omit<ComponentProps<typeof SelectCombobox>, "options"> & { current?: string }) {
  const options = useMemo(() => timezones(current).map((zone) => ({ value: zone, label: zone.replaceAll("_", " ") })), [current]);
  return <SelectCombobox className="w-full" {...props} options={options} />;
}
