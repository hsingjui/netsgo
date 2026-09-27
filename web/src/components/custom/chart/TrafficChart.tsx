import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Line, LineChart, CartesianGrid, XAxis, YAxis } from 'recharts';
import { AlertCircle, Activity } from 'lucide-react';

import { cn } from '@/lib/utils';
import {
  type ChartConfig,
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  ChartLegend,
  ChartLegendContent,
} from '@/components/ui/chart';
import { useClientTraffic } from '@/hooks/use-client-traffic';
import { formatTrafficRate } from '@/lib/format';
import { getTrafficSeriesKey, getTunnelSeriesKey } from '@/lib/tunnel-traffic-keys';
import type { ResourceScope } from '@/lib/resource-scope';
import type { ClientTrafficRange, ClientTrafficResponse, ProxyConfig, ProxyType } from '@/types';

interface TrafficChartProps {
  scope: ResourceScope;
  clientId: string;
  tunnels: ProxyConfig[];
}

type TunnelMeta = {
  key: string;
  name: string;
  type: ProxyType;
  color: string;
};

type ChartRow = {
  timestamp: number;
  [key: string]: number;
};

const RANGE_OPTIONS: Array<{ value: ClientTrafficRange; label: string }> = [
  { value: '60s', label: '60s' },
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
];

const CHART_COLORS = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
] as const;

// Each range renders an average rate (bytes/sec): the total bytes inside one displayed
// bucket are divided by bucketSeconds. gridBucketMs/gridPointCount build a dense, evenly
// spaced timeline that is zero-filled and, for coarser ranges, aggregates finer source
// buckets into it. 60s has no grid because the server already returns a dense per-second
// series. Ranges not listed here (e.g. 1h) are intentionally unsupported by this chart
// until they get a RANGE_OPTIONS entry and a getRangeSummary case.
const RANGE_CHART_CONFIG: Partial<Record<ClientTrafficRange, {
  bucketSeconds: number;
  gridBucketMs?: number;
  gridPointCount?: number;
}>> = {
  '60s': { bucketSeconds: 1 },
  '24h': { bucketSeconds: 300, gridBucketMs: 300_000, gridPointCount: 24 * 12 },
  '7d': { bucketSeconds: 3_600, gridBucketMs: 3_600_000, gridPointCount: 7 * 24 },
};

function getTunnelColor(index: number) {
  return CHART_COLORS[index] ?? `hsl(${(index * 67) % 360} 72% 58%)`;
}

function getTrafficSeriesName(item: ClientTrafficResponse['items'][number], t: ReturnType<typeof useTranslation>['t']) {
  return item.tunnel_name ?? t('traffic.deletedTunnel', { id: item.tunnel_id ?? 'unknown' });
}

function formatXAxisLabel(timestamp: number, range: ClientTrafficRange, language: string) {
  const date = new Date(timestamp);
  if (range === '60s') {
    return date.toLocaleString(language, { minute: '2-digit', second: '2-digit' });
  }
  return date.toLocaleString(language, range === '24h'
    ? { hour: '2-digit', minute: '2-digit' }
    : { month: 'numeric', day: 'numeric', hour: '2-digit' });
}

function formatTooltipLabel(timestamp: number, range: ClientTrafficRange, language: string) {
  const date = new Date(timestamp);
  if (range === '60s') {
    return date.toLocaleString(language, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }
  return date.toLocaleString(language, range === '24h'
    ? { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { year: 'numeric', month: 'numeric', day: 'numeric', hour: '2-digit' });
}

function getRangeSummary(range: ClientTrafficRange, t: ReturnType<typeof useTranslation>['t']) {
  switch (range) {
    case '60s':
      return t('traffic.range60s');
    case '24h':
      return t('traffic.range24h');
    case '7d':
      return t('traffic.range7d');
    default:
      return t('traffic.rangeDefault');
  }
}

function getErrorMessage(error: unknown, t: ReturnType<typeof useTranslation>['t']) {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return t('traffic.loadFailed');
}

function buildGridTimestamps(range: ClientTrafficRange, nowMs = Date.now()) {
  const config = RANGE_CHART_CONFIG[range];
  const { gridBucketMs, gridPointCount } = config ?? {};
  if (!gridBucketMs || !gridPointCount) {
    return [];
  }

  const endTimestamp = Math.floor(nowMs / gridBucketMs) * gridBucketMs;
  return Array.from({ length: gridPointCount }, (_, index) => (
    endTimestamp - (gridPointCount - index - 1) * gridBucketMs
  ));
}

function buildTrafficTrendChartState(
  data: ClientTrafficResponse | undefined,
  tunnels: (Pick<ProxyConfig, 'name' | 'type'> & Partial<Pick<ProxyConfig, 'id'>>)[],
  range: ClientTrafficRange,
  t: ReturnType<typeof useTranslation>['t'],
) {
  const knownTunnels = new Map<string, Pick<TunnelMeta, 'key' | 'name' | 'type'>>();

  for (const tunnel of tunnels) {
    const seriesKey = getTunnelSeriesKey(tunnel);
    knownTunnels.set(seriesKey, {
      key: seriesKey,
      name: tunnel.name,
      type: tunnel.type,
    });
  }

  for (const item of data?.items ?? []) {
    const seriesKey = getTrafficSeriesKey(item);
    if (!knownTunnels.has(seriesKey)) {
      knownTunnels.set(seriesKey, {
        key: seriesKey,
        name: getTrafficSeriesName(item, t),
        type: item.tunnel_type ?? 'tcp',
      });
    }
  }

  const tunnelSeries: TunnelMeta[] = Array.from(knownTunnels.values())
    .sort((left, right) => {
      if (left.name !== right.name) {
        return left.name.localeCompare(right.name);
      }
      return left.type.localeCompare(right.type);
    })
    .map((tunnel, index) => ({
      key: tunnel.key,
      name: tunnel.name,
      type: tunnel.type,
      color: getTunnelColor(index),
    }));

  const chartConfig = tunnelSeries.reduce<ChartConfig>((config, tunnel) => {
    config[tunnel.key] = {
      label: `${tunnel.name} · ${tunnel.type.toUpperCase()}`,
      color: tunnel.color,
    };
    return config;
  }, {});

  const { bucketSeconds = 1, gridBucketMs } = RANGE_CHART_CONFIG[range] ?? {};
  const pointsByTunnel = new Map<string, Map<number, number>>();
  const timestamps = new Set<number>(buildGridTimestamps(range));

  for (const item of data?.items ?? []) {
    const seriesKey = getTrafficSeriesKey(item);
    let pointMap = pointsByTunnel.get(seriesKey);
    if (!pointMap) {
      pointMap = new Map<number, number>();
      pointsByTunnel.set(seriesKey, pointMap);
    }

    for (const point of item.points) {
      const rawTimestamp = new Date(point.bucket_start).getTime();
      const bucketTimestamp = gridBucketMs
        ? Math.floor(rawTimestamp / gridBucketMs) * gridBucketMs
        : rawTimestamp;
      pointMap.set(bucketTimestamp, (pointMap.get(bucketTimestamp) ?? 0) + point.total_bytes);
      timestamps.add(bucketTimestamp);
    }
  }

  const chartData = Array.from(timestamps)
    .sort((a, b) => a - b)
    .map<ChartRow>((timestamp) => {
      const row: ChartRow = { timestamp };
      for (const tunnel of tunnelSeries) {
        const bytes = pointsByTunnel.get(tunnel.key)?.get(timestamp) ?? 0;
        row[tunnel.key] = bytes / bucketSeconds;
      }
      return row;
    });

  return {
    chartConfig,
    chartData,
    tunnelSeries,
  };
}

export function TrafficChart({ scope, clientId, tunnels }: TrafficChartProps) {
  const { t, i18n } = useTranslation();
  const [range, setRange] = useState<ClientTrafficRange>('60s');
  const { data, isLoading, isError, error } = useClientTraffic(scope, clientId, range);

  const { chartConfig, chartData, tunnelSeries } = useMemo(
    () => buildTrafficTrendChartState(data, tunnels, range, t),
    [data, tunnels, range, t],
  );

  const hasTunnels = tunnelSeries.length > 0;
  const hasTrafficData = chartData.length > 0;

  return (
    <div className="rounded-xl border border-border/40 bg-card/30 p-6 shadow-sm backdrop-blur-sm">
      <div className="mb-5 flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2 text-lg font-semibold text-foreground">
            <Activity className="h-5 w-5 text-primary" />
            <h3>{t('traffic.trendTitle')}</h3>
          </div>
          <p className="text-sm text-muted-foreground">
            {getRangeSummary(range, t)} · {t('traffic.tunnelCount', { count: tunnelSeries.length })}
          </p>
        </div>

        <div className="inline-flex items-center gap-1 self-start rounded-lg border border-border/60 bg-muted/40 p-1">
          {RANGE_OPTIONS.map((option) => {
            const active = option.value === range;
            return (
              <button
                key={option.value}
                type="button"
                aria-pressed={active}
                onClick={() => setRange(option.value)}
                className={cn(
                  'rounded-md px-3 py-1 text-xs font-medium transition-colors',
                  active
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {option.label}
              </button>
            );
          })}
        </div>
      </div>

      {!hasTunnels ? (
        <div className="flex h-72 flex-col items-center justify-center rounded-xl border border-dashed border-border/60 bg-background/30 text-center">
          <p className="text-sm font-medium text-foreground">{t('traffic.noClientTunnels')}</p>
          <p className="mt-1 text-sm text-muted-foreground">{t('traffic.noClientTunnelsHelp')}</p>
        </div>
      ) : isLoading ? (
        <div className="h-72 animate-pulse rounded-xl border border-border/60 bg-background/30" />
      ) : isError ? (
        <div className="flex h-72 flex-col items-center justify-center rounded-xl border border-dashed border-destructive/30 bg-destructive/5 text-center">
          <AlertCircle className="mb-3 h-5 w-5 text-destructive" />
          <p className="text-sm font-medium text-foreground">{t('traffic.loadFailed')}</p>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">{getErrorMessage(error, t)}</p>
        </div>
      ) : !hasTrafficData ? (
        <div className="flex h-72 flex-col items-center justify-center rounded-xl border border-dashed border-border/60 bg-background/30 text-center">
          <p className="text-sm font-medium text-foreground">{t('traffic.emptyRange')}</p>
        </div>
      ) : (
        <div className="h-80 w-full">
          <ChartContainer config={chartConfig} className="h-full w-full">
            <LineChart data={chartData} margin={{ top: 12, right: 12, left: 0, bottom: 4 }}>
              <CartesianGrid vertical={false} stroke="var(--border)" strokeDasharray="3 3" strokeOpacity={0.45} />
              <XAxis
                dataKey="timestamp"
                axisLine={false}
                tickLine={false}
                tickMargin={12}
                minTickGap={28}
                tickFormatter={(value) => formatXAxisLabel(Number(value), range, i18n.language)}
              />
              <YAxis
                axisLine={false}
                tickLine={false}
                tickMargin={10}
                width="auto"
                domain={[0, 'auto']}
                tickFormatter={(value) => formatTrafficRate(Number(value))}
              />
              <ChartTooltip
                content={(
                  <ChartTooltipContent
                    indicator="line"
                    labelFormatter={(_, payload) => {
                      const timestamp = payload?.[0]?.payload?.timestamp;
                      return typeof timestamp === 'number'
                        ? formatTooltipLabel(timestamp, range, i18n.language)
                        : '';
                    }}
                    formatter={(value, name) => (
                      <>
                        <span className="text-muted-foreground">{chartConfig[String(name)]?.label ?? String(name)}</span>
                        <span className="font-mono font-medium text-foreground tabular-nums">
                          {formatTrafficRate(Number(value))}
                        </span>
                      </>
                    )}
                  />
                )}
              />
              {tunnelSeries.map((tunnel) => (
                <Line
                  key={tunnel.key}
                  type="monotone"
                  dataKey={tunnel.key}
                  name={tunnel.key}
                  stroke={tunnel.color}
                  strokeWidth={2}
                  dot={false}
                  activeDot={{ r: 4 }}
                  isAnimationActive={false}
                  connectNulls
                />
              ))}
              <ChartLegend content={<ChartLegendContent className="flex-wrap gap-x-4 gap-y-1" />} />
            </LineChart>
          </ChartContainer>
        </div>
      )}
    </div>
  );
}
