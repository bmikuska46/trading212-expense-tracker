"use client";

import { useMemo, type ComponentProps } from "react";
import { timezones } from "@/lib/format";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";

// TimezoneSelect lists the IANA zones the browser knows. The server validates
// the choice against its own time zone database.
export function TimezoneSelect({ current, ...props }: ComponentProps<typeof NativeSelect> & { current?: string }) {
  const zones = useMemo(() => timezones(current), [current]);
  return <NativeSelect className="w-full" {...props}>
    {zones.map((zone) => <NativeSelectOption key={zone} value={zone}>{zone.replaceAll("_", " ")}</NativeSelectOption>)}
  </NativeSelect>;
}
