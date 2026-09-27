import { i18n, DEFAULT_LOCALE } from '@/i18n';
import { formatRelativeTimestamp } from '@/lib/format';

function locale() {
  return i18n.resolvedLanguage || i18n.language || DEFAULT_LOCALE;
}

export function formatActivityRelativeTime(value: string, now = Date.now()) {
  return formatRelativeTimestamp(value, now);
}

export function formatActivityAbsoluteTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return i18n.t('format.unknownTime');
  return new Intl.DateTimeFormat(locale(), { dateStyle: 'full', timeStyle: 'long' }).format(date);
}

export function formatActivityClock(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '--:--';
  return new Intl.DateTimeFormat(locale(), { hour: '2-digit', minute: '2-digit', hour12: false }).format(date);
}

function startOfDay(timestamp: number) {
  const date = new Date(timestamp);
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

export function formatActivityDay(value: string, now = Date.now()) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return i18n.t('format.unknownTime');
  const monthDay = new Intl.DateTimeFormat(locale(), date.getFullYear() === new Date(now).getFullYear()
    ? { month: 'long', day: 'numeric' }
    : { year: 'numeric', month: 'long', day: 'numeric' }).format(date);
  const dayDelta = Math.round((startOfDay(now) - startOfDay(date.getTime())) / 86_400_000);
  if (dayDelta === 0) return `${i18n.t('activity.today')} · ${monthDay}`;
  if (dayDelta === 1) return `${i18n.t('activity.yesterday')} · ${monthDay}`;
  return `${monthDay} · ${new Intl.DateTimeFormat(locale(), { weekday: 'short' }).format(date)}`;
}

export function activityDateInputValue(date: Date) {
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${date.getFullYear()}-${month}-${day}`;
}

export function activityDateShortLabel(value: string) {
  const date = new Date(`${value}T00:00:00`);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(locale(), { month: 'numeric', day: 'numeric' }).format(date);
}

export function activityDayKey(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return 'unknown';
  return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
}
