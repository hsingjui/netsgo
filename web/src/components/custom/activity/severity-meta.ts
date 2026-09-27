import { AlertTriangle, Bug, CircleAlert, Info } from 'lucide-react';
import type { LucideIcon } from 'lucide-react';

import type { ActivityCategory, ActivitySeverity } from '@/types';

export const allSeverities: ActivitySeverity[] = ['debug', 'info', 'warning', 'error'];

export interface ActivityFilterValue {
  severities: ActivitySeverity[];
  categories: ActivityCategory[];
  fromDate?: string;
  toDate?: string;
}

export const defaultActivityFilter: ActivityFilterValue = {
  severities: ['info', 'warning', 'error'],
  categories: [],
};

export const severityIcon: Record<ActivitySeverity, LucideIcon> = {
  debug: Bug,
  info: Info,
  warning: AlertTriangle,
  error: CircleAlert,
};

export const severityActiveClass: Record<ActivitySeverity, string> = {
  debug: 'border-slate-400/30 bg-slate-500/10 text-slate-600 dark:text-slate-300',
  info: 'border-sky-400/30 bg-sky-500/10 text-sky-700 dark:text-sky-300',
  warning: 'border-amber-400/30 bg-amber-500/10 text-amber-700 dark:text-amber-300',
  error: 'border-rose-400/30 bg-rose-500/10 text-rose-700 dark:text-rose-300',
};
