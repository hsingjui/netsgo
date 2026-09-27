import { CalendarDays, Check, ChevronDown, RotateCcw } from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Separator } from '@/components/ui/separator';
import { activityDateInputValue, activityDateShortLabel } from '@/lib/activity-format';
import { cn } from '@/lib/utils';
import { allSeverities, defaultActivityFilter, severityActiveClass, severityIcon, type ActivityFilterValue } from './severity-meta';
import type { ActivityCategory } from '@/types';

const categories: ActivityCategory[] = ['client', 'tunnel', 'p2p', 'admin', 'security'];

type RangePreset = 'all' | 'today' | 'last7d' | 'last30d';

const rangePresets: RangePreset[] = ['all', 'today', 'last7d', 'last30d'];

function presetRange(preset: RangePreset): Pick<ActivityFilterValue, 'fromDate' | 'toDate'> {
  if (preset === 'all') return { fromDate: undefined, toDate: undefined };
  const today = new Date();
  const from = new Date(today);
  if (preset === 'last7d') from.setDate(from.getDate() - 6);
  if (preset === 'last30d') from.setDate(from.getDate() - 29);
  return { fromDate: activityDateInputValue(from), toDate: activityDateInputValue(today) };
}

function matchedPreset(value: ActivityFilterValue): RangePreset | undefined {
  return rangePresets.find((preset) => {
    const range = presetRange(preset);
    return range.fromDate === value.fromDate && range.toDate === value.toDate;
  });
}

function rangeLabel(value: ActivityFilterValue, t: TFunction) {
  const preset = matchedPreset(value);
  if (preset) return t(`activity.range.${preset}`);
  const from = value.fromDate ? activityDateShortLabel(value.fromDate) : '';
  const to = value.toDate ? activityDateShortLabel(value.toDate) : '';
  if (from && to) return `${from} → ${to}`;
  return from ? `${from} →` : `→ ${to}`;
}

function isFilterDirty(value: ActivityFilterValue) {
  return Boolean(value.fromDate || value.toDate)
    || value.categories.length > 0
    || value.severities.length !== defaultActivityFilter.severities.length
    || !defaultActivityFilter.severities.every((severity) => value.severities.includes(severity));
}

function FilterChip({ active, activeClass, icon: Icon, label, onToggle }: {
  active: boolean;
  activeClass?: string;
  icon?: LucideIcon;
  label: string;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onToggle}
      className={cn(
        'inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md border px-2 text-xs transition-colors',
        active
          ? (activeClass ?? 'border-foreground/15 bg-foreground/[0.06] font-medium text-foreground')
          : 'border-transparent text-muted-foreground hover:bg-muted/60 hover:text-foreground',
      )}
    >
      {Icon ? <Icon className="size-3.5" strokeWidth={2} /> : null}
      {label}
    </button>
  );
}

function RangeFilter({ value, onChange }: { value: ActivityFilterValue; onChange: (value: ActivityFilterValue) => void }) {
  const { t } = useTranslation();
  const preset = matchedPreset(value);

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className="h-7 gap-1.5 border-border/60 bg-transparent px-2 text-xs font-normal shadow-none">
          <CalendarDays className="size-3.5 text-muted-foreground" />
          {rangeLabel(value, t)}
          <ChevronDown className="size-3 text-muted-foreground/60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="gap-2 p-2">
        <div className="grid gap-0.5">
          {rangePresets.map((entry) => (
            <button
              key={entry}
              type="button"
              onClick={() => onChange({ ...value, ...presetRange(entry) })}
              className={cn(
                'flex h-8 items-center justify-between rounded-md px-2 text-xs transition-colors',
                preset === entry ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground',
              )}
            >
              {t(`activity.range.${entry}`)}
              {preset === entry ? <Check className="size-3.5" /> : null}
            </button>
          ))}
        </div>
        <Separator />
        <div className="grid gap-1.5">
          <span className="text-[11px] text-muted-foreground">{t('activity.range.custom')}</span>
          <label className="flex items-center gap-2">
            <span className="w-10 shrink-0 text-[11px] text-muted-foreground">{t('activity.fromDate')}</span>
            <Input
              type="date"
              max={value.toDate}
              value={value.fromDate ?? ''}
              onChange={(event) => onChange({ ...value, fromDate: event.target.value || undefined })}
              className="h-8 min-w-0 flex-1 text-xs"
            />
          </label>
          <label className="flex items-center gap-2">
            <span className="w-10 shrink-0 text-[11px] text-muted-foreground">{t('activity.toDate')}</span>
            <Input
              type="date"
              min={value.fromDate}
              value={value.toDate ?? ''}
              onChange={(event) => onChange({ ...value, toDate: event.target.value || undefined })}
              className="h-8 min-w-0 flex-1 text-xs"
            />
          </label>
        </div>
      </PopoverContent>
    </Popover>
  );
}

export function ActivityFilters({ value, onChange, showRange = true, auxiliaryFilters }: {
  value: ActivityFilterValue;
  onChange: (value: ActivityFilterValue) => void;
  showRange?: boolean;
  auxiliaryFilters?: ReactNode;
}) {
  const { t } = useTranslation();
  const toggle = <T extends string>(items: T[], item: T) => items.includes(item) ? items.filter((entry) => entry !== item) : [...items, item];
  const allSeveritiesActive = allSeverities.every((severity) => value.severities.includes(severity));

  return (
    <div className="grid gap-2.5">
      <div className="grid grid-cols-[2.5rem_minmax(0,1fr)] items-center gap-x-1">
        <span className="shrink-0 text-xs text-muted-foreground">{t('activity.filterSeverity')}</span>
        <div className="flex min-w-0 gap-1 overflow-x-auto overscroll-x-contain pb-px [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          <FilterChip
            active={allSeveritiesActive}
            label={t('activity.filterAll')}
            onToggle={() => onChange({ ...value, severities: [...allSeverities] })}
          />
          {allSeverities.map((severity) => (
            <FilterChip
              key={severity}
              active={value.severities.includes(severity)}
              activeClass={severityActiveClass[severity]}
              icon={severityIcon[severity]}
              label={t(`activity.severity.${severity}`)}
              onToggle={() => {
                const next = toggle(value.severities, severity);
                if (next.length === 0) return;
                onChange({ ...value, severities: next });
              }}
            />
          ))}
        </div>
      </div>
      <div className="grid grid-cols-[2.5rem_minmax(0,1fr)] items-center gap-x-1">
        <span className="shrink-0 text-xs text-muted-foreground">{t('activity.filterCategory')}</span>
        <div className="flex min-w-0 gap-1 overflow-x-auto overscroll-x-contain pb-px [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          <FilterChip
            active={value.categories.length === 0}
            label={t('activity.filterAll')}
            onToggle={() => onChange({ ...value, categories: [] })}
          />
          {categories.map((category) => (
            <FilterChip
              key={category}
              active={value.categories.includes(category)}
              label={t(`activity.category.${category}`)}
              onToggle={() => onChange({ ...value, categories: toggle(value.categories, category) })}
            />
          ))}
        </div>
      </div>
      {showRange || auxiliaryFilters || isFilterDirty(value) ? (
        <div className="flex flex-col gap-2 border-t border-border/40 pt-2.5 sm:flex-row sm:items-center">
          <div className="flex flex-wrap items-center gap-1.5">
            {showRange ? <RangeFilter value={value} onChange={onChange} /> : null}
            {isFilterDirty(value) ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 gap-1.5 px-2 text-xs font-normal text-muted-foreground hover:text-foreground"
                onClick={() => onChange({ ...defaultActivityFilter })}
              >
                <RotateCcw className="size-3.5" />
                {t('activity.filterReset')}
              </Button>
            ) : null}
          </div>
          {auxiliaryFilters ? <div className="w-full sm:ml-auto sm:w-52">{auxiliaryFilters}</div> : null}
        </div>
      ) : null}
    </div>
  );
}
