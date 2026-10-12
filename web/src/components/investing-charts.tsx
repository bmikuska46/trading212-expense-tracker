"use client";

// Investing charts load separately, so other pages never download them. They
// are plain SVG lines on one shared axis: a second scale would invent
// correlations. A crosshair and tooltip follow the pointer and the arrow keys.
import { useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { useTranslations } from "next-intl";
import { dateFormat } from "@/lib/format";
import { cn } from "@/lib/utils";

// Values are aligned with the chart times. Null breaks the line, so a pie
// that did not exist yet starts where its first snapshot is.
export type Series = { key: string; label: string; color: string; dashed?: boolean; area?: boolean; values: (number | null)[] };

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => setWidth(Math.floor(entry.contentRect.width)));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

// scale pads the values a little and rounds the ends to steps of 1, 2, 2.5
// or 5 times a power of ten.
function scale(values: number[], zero: boolean) {
  let low = Math.min(...values, ...(zero ? [0] : []));
  let high = Math.max(...values, ...(zero ? [0] : []));
  const span = high - low || Math.abs(high) || 1;
  if (!zero || low < 0) low -= span * 0.04;
  if (!zero || high > 0) high += span * 0.04;
  const raw = (high - low) / 4;
  const power = 10 ** Math.floor(Math.log10(raw));
  const step = ([1, 2, 2.5, 5, 10].find((unit) => raw <= unit * power) ?? 10) * power;
  const first = Math.floor(low / step) * step;
  const last = Math.max(Math.ceil(high / step) * step, first + step);
  const list: number[] = [];
  for (let value = first; value <= last + step / 2; value += step) list.push(Math.round(value / step) * step);
  return list;
}

const day = dateFormat({ month: "short", day: "numeric" });
const dayYear = dateFormat({ month: "short", day: "numeric", year: "numeric" });
const moment = dateFormat({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

function Mark({ color, dashed }: { color: string; dashed?: boolean }) {
  return <span aria-hidden="true" className={cn("inline-block size-2 shrink-0 rounded-[2px]", dashed && "border")}
    style={dashed ? { borderColor: color } : { background: color }} />;
}

export function LineChart({ times, series, format, axis, zero = false, label }: {
  times: number[]; series: Series[]; format: (value: number) => string; axis: (value: number) => string; zero?: boolean; label: string;
}) {
  const t = useTranslations("investing.charts");
  const [frame, width] = useWidth<HTMLDivElement>();
  const [active, setActive] = useState<number | null>(null);
  const values = series.flatMap((line) => line.values.filter((value) => value !== null));
  const height = width < 480 ? 200 : 240;
  // Direct labels at the line ends name the latest figures. Past four lines
  // they crowd out, and the tooltip carries them instead.
  const labelled = width >= 480 && series.length <= 4;
  const margin = { top: 10, right: labelled ? 76 : 8, bottom: 24, left: 60 };
  const plot = { width: Math.max(0, width - margin.left - margin.right), height: height - margin.top - margin.bottom };
  if (values.length === 0) {
    return <div ref={frame} className="grid place-items-center text-sm text-muted-foreground" style={{ minHeight: height }}>
      {t("noValues")}
    </div>;
  }
  const ticks = scale(values, zero);
  const low = ticks[0];
  const high = ticks[ticks.length - 1];
  const start = times[0];
  const end = times[times.length - 1];
  const x = (time: number) => margin.left + (end === start ? plot.width / 2 : ((time - start) / (end - start)) * plot.width);
  const y = (value: number) => margin.top + plot.height - ((value - low) / (high - low)) * plot.height;
  const short = end - start <= 8 * 86_400_000;
  const longSpan = end - start > 300 * 86_400_000;
  const tickCount = Math.max(1, Math.min(width < 480 ? 3 : 5, times.length));
  const timeTicks = tickCount === 1 ? [start] : Array.from({ length: tickCount }, (_, index) => start + ((end - start) * index) / (tickCount - 1));
  const tickLabel = (time: number) => (short ? moment : longSpan ? dayYear : day).format(time);
  const last = times.length - 1;

  const path = (line: Series) => {
    let d = "";
    let pen = false;
    line.values.forEach((value, index) => {
      if (value === null) { pen = false; return; }
      d += `${pen ? "L" : "M"}${x(times[index])},${y(value)}`;
      pen = true;
    });
    return d;
  };
  const area = (line: Series) => {
    const points = line.values.flatMap((value, index) => value === null ? [] : [[x(times[index]), y(value)]]);
    if (points.length < 2) return "";
    const base = y(Math.min(high, Math.max(low, 0)));
    return `M${points.map(([px, py]) => `${px},${py}`).join("L")}L${points[points.length - 1][0]},${base}L${points[0][0]},${base}Z`;
  };

  // End labels are spread apart so close values stay readable.
  const ends = labelled ? series.flatMap((line) => {
    const value = line.values[last];
    return value === null ? [] : [{ line, value, top: y(value) }];
  }).sort((a, b) => a.top - b.top) : [];
  for (let index = 1; index < ends.length; index++) ends[index].top = Math.max(ends[index].top, ends[index - 1].top + 14);

  const point = (event: PointerEvent<SVGSVGElement>) => {
    const left = event.clientX - event.currentTarget.getBoundingClientRect().left;
    let best = 0;
    times.forEach((time, index) => { if (Math.abs(x(time) - left) < Math.abs(x(times[best]) - left)) best = index; });
    setActive(best);
  };
  const move = (event: KeyboardEvent<HTMLDivElement>) => {
    const next = { ArrowLeft: (active ?? last + 1) - 1, ArrowRight: (active ?? -1) + 1, Home: 0, End: last }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    setActive(Math.min(last, Math.max(0, next)));
  };
  // A shorter range can arrive while a later point is still active.
  const current = active !== null && active <= last ? active : null;
  const shown = current === null ? [] : series.filter((line) => line.values[current] !== null);
  const tooltipWidth = 224;
  const tooltipLeft = current === null ? 0 : Math.max(0, Math.min(width - tooltipWidth, x(times[current]) - tooltipWidth / 2));

  return <div ref={frame} tabIndex={0} role="group" onKeyDown={move}
    aria-label={t("label", { label })}
    onFocus={() => setActive((value) => value ?? last)} onBlur={() => setActive(null)}
    className="relative rounded-md focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring" style={{ minHeight: height }}>
    {width > 0 && <svg width={width} height={height} aria-hidden="true" className="block overflow-visible"
      onPointerMove={point} onPointerLeave={() => setActive(null)}>
      {ticks.map((value) => <g key={value}>
        <line x1={margin.left} x2={margin.left + plot.width} y1={y(value)} y2={y(value)}
          className={value === 0 && zero ? "stroke-foreground/30" : "stroke-border"} strokeWidth={1} shapeRendering="crispEdges" />
        <text x={margin.left - 8} y={y(value)} dy="0.32em" textAnchor="end" className="fill-faint font-mono text-[0.6875rem]">{axis(value)}</text>
      </g>)}
      {timeTicks.map((time, index) => <text key={index} x={x(time)} y={height - 6}
        textAnchor={tickCount === 1 ? "middle" : index === 0 ? "start" : index === tickCount - 1 ? "end" : "middle"}
        className="fill-faint font-mono text-[0.6875rem]">{tickLabel(time)}</text>)}
      {series.map((line) => line.area && <path key={`${line.key}-area`} d={area(line)} style={{ fill: line.color }} fillOpacity={0.12} />)}
      {series.map((line) => <path key={line.key} d={path(line)} fill="none" style={{ stroke: line.color }} strokeWidth={line.dashed ? 1.5 : 2}
        strokeLinejoin="round" strokeLinecap="round" strokeDasharray={line.dashed ? "5 4" : undefined} />)}
      {series.map((line) => {
        const value = line.values[last];
        return value !== null && <circle key={`${line.key}-end`} cx={x(times[last])} cy={y(value)} r={3}
          style={{ fill: line.color }} className="stroke-card" strokeWidth={2} />;
      })}
      {ends.map(({ line, value, top }) => <text key={`${line.key}-label`} x={margin.left + plot.width + 8} y={top} dy="0.32em"
        className="fill-muted-foreground font-mono text-[0.6875rem]">{format(value)}</text>)}
      {current !== null && <>
        <line x1={x(times[current])} x2={x(times[current])} y1={margin.top} y2={margin.top + plot.height}
          className="stroke-foreground/40" strokeWidth={1} shapeRendering="crispEdges" />
        {shown.map((line) => <circle key={`${line.key}-active`} cx={x(times[current])} cy={y(line.values[current]!)} r={4}
          style={{ fill: line.color }} className="stroke-card" strokeWidth={2} />)}
      </>}
      <rect x={margin.left} y={margin.top} width={plot.width} height={plot.height} fill="transparent" />
    </svg>}
    {current !== null && <div role="presentation"
      className="pointer-events-none absolute top-0 z-10 rounded-md border bg-popover p-2.5 text-xs text-popover-foreground shadow-lg"
      style={{ left: tooltipLeft, width: tooltipWidth }}>
      <p className="font-medium">{moment.format(times[current])}</p>
      <dl className="mt-1.5 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1">
        {shown.map((line) => <div key={line.key} className="contents">
          <dt className="flex min-w-0 items-center gap-2 text-muted-foreground"><Mark color={line.color} dashed={line.dashed} />
            <span className="truncate">{line.label}</span></dt>
          <dd className="text-right font-mono">{format(line.values[current]!)}</dd>
        </div>)}
      </dl>
    </div>}
    <p aria-live="polite" className="sr-only">
      {current === null ? "" : t("live", { time: moment.format(times[current]), values: shown.map((line) => `${line.label} ${format(line.values[current]!)}`).join(", ") })}
    </p>
  </div>;
}
