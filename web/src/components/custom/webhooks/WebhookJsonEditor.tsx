import { autocompletion, type CompletionContext } from '@codemirror/autocomplete';
import { json, jsonParseLinter } from '@codemirror/lang-json';
import { linter, lintGutter } from '@codemirror/lint';
import { Compartment, EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { basicSetup } from 'codemirror';
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';

import { cn } from '@/lib/utils';
import {
  getWebhookVariables,
  getTemplateIssues,
  webhookVariableSample,
} from './webhook-template';
import type { ActivityWebhookConfig, WebhookCatalog, WebhookEventKey } from '@/types/webhook';

export interface WebhookJsonEditorHandle {
  focus: () => void;
  format: () => boolean;
}

interface WebhookJsonEditorProps {
  value: string;
  onChange: (value: string) => void;
  invalid?: boolean;
  className?: string;
  events: WebhookEventKey[];
  sampleEvent: WebhookEventKey;
  webhook: Pick<ActivityWebhookConfig, 'id' | 'name'>;
  catalog: WebhookCatalog;
  label: string;
}

function webhookVariableCompletion(
  events: WebhookEventKey[],
  sampleEvent: WebhookEventKey,
  webhook: Pick<ActivityWebhookConfig, 'id' | 'name'>,
  catalog: WebhookCatalog,
) {
  return (context: CompletionContext) => {
    const variable = context.matchBefore(/{{[\w.-]*$/);
    if (!variable && !context.explicit) return null;
    return {
      from: variable?.from ?? context.pos,
      options: getWebhookVariables(catalog, events, 'body')
        .map((entry) => ({
          label: `{{${entry.key}}}`,
          apply: `{{${entry.key}}}`,
          type: 'variable',
          detail: webhookVariableSample(catalog, entry, sampleEvent, webhook),
          boost: entry.group === 'event' ? 2 : 1,
        })),
    };
  };
}

function templateExtensions(
  events: WebhookEventKey[],
  sampleEvent: WebhookEventKey,
  webhook: Pick<ActivityWebhookConfig, 'id' | 'name'>,
  catalog: WebhookCatalog,
  label: string,
  t: TFunction,
) {
  return [
    linter((view) => getTemplateIssues(view.state.doc.toString(), events, 'body', catalog.variables).map((issue) => ({
      from: issue.from,
      to: issue.to,
      severity: 'error',
      message: t(`webhooks.validation.${issue.code}`, { key: issue.key }),
    }))),
    autocompletion({ override: [webhookVariableCompletion(events, sampleEvent, webhook, catalog)] }),
    EditorView.contentAttributes.of({ 'aria-label': label }),
  ];
}

const webhookEditorTheme = EditorView.theme({
  '&': {
    minHeight: '240px',
    backgroundColor: 'transparent',
    color: 'var(--foreground)',
    fontSize: '12px',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': {
    minHeight: '240px',
    maxHeight: '420px',
    overflow: 'auto',
    fontFamily: 'JetBrains Mono Variable, JetBrains Mono, monospace',
  },
  '.cm-content': {
    minHeight: '240px',
    padding: '10px 0',
    caretColor: 'var(--foreground)',
  },
  '.cm-line': { padding: '0 12px 0 8px' },
  '.cm-gutters': {
    backgroundColor: 'color-mix(in oklab, var(--muted) 45%, transparent)',
    color: 'var(--muted-foreground)',
    border: 'none',
    paddingLeft: '4px',
  },
  '.cm-activeLine, .cm-activeLineGutter': {
    backgroundColor: 'color-mix(in oklab, var(--muted) 60%, transparent)',
  },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': {
    backgroundColor: 'color-mix(in oklab, var(--primary) 20%, transparent) !important',
  },
  '.cm-tooltip': {
    zIndex: '70',
    border: '1px solid var(--border)',
    borderRadius: '8px',
    backgroundColor: 'var(--popover)',
    color: 'var(--popover-foreground)',
    boxShadow: '0 8px 24px rgb(0 0 0 / 0.12)',
    overflow: 'hidden',
  },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': {
    backgroundColor: 'var(--accent)',
    color: 'var(--accent-foreground)',
  },
  '.cm-diagnostic-error': { borderLeftColor: 'var(--destructive)' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecoration: 'underline wavy var(--destructive)' },
}, { dark: false });

export const WebhookJsonEditor = forwardRef<WebhookJsonEditorHandle, WebhookJsonEditorProps>(
  function WebhookJsonEditor({ value, onChange, invalid = false, className, events, sampleEvent, webhook, catalog, label }, forwardedRef) {
    const { t } = useTranslation();
    const hostRef = useRef<HTMLDivElement>(null);
    const viewRef = useRef<EditorView | null>(null);
    const onChangeRef = useRef(onChange);
    const [templateCompartment] = useState(() => new Compartment());
    const [initialValue] = useState(value);

    useEffect(() => {
      onChangeRef.current = onChange;
    }, [onChange]);

    useEffect(() => {
      if (!hostRef.current) return;
      const state = EditorState.create({
        doc: initialValue,
        extensions: [
          basicSetup,
          json(),
          linter(jsonParseLinter()),
          lintGutter(),
          templateCompartment.of([]),
          EditorView.lineWrapping,
          webhookEditorTheme,
          EditorView.updateListener.of((update) => {
            if (update.docChanged) onChangeRef.current(update.state.doc.toString());
          }),
        ],
      });
      const view = new EditorView({ state, parent: hostRef.current });
      viewRef.current = view;
      return () => {
        view.destroy();
        viewRef.current = null;
      };
    }, [initialValue, templateCompartment]);

    useEffect(() => {
      const view = viewRef.current;
      if (!view) return;
      view.dispatch({ effects: templateCompartment.reconfigure(templateExtensions(events, sampleEvent, webhook, catalog, label, t)) });
    }, [catalog, events, label, sampleEvent, t, templateCompartment, webhook]);

    useEffect(() => {
      const view = viewRef.current;
      if (!view) return;
      const current = view.state.doc.toString();
      if (current === value) return;
      view.dispatch({ changes: { from: 0, to: current.length, insert: value } });
    }, [value]);

    useImperativeHandle(forwardedRef, () => ({
      focus: () => viewRef.current?.focus(),
      format: () => {
        const view = viewRef.current;
        if (!view) return false;
        try {
          const current = view.state.doc.toString();
          const formatted = JSON.stringify(JSON.parse(current), null, 2);
          view.dispatch({ changes: { from: 0, to: current.length, insert: formatted } });
          return true;
        } catch {
          return false;
        }
      },
    }), []);

    return (
      <div
        ref={hostRef}
        data-invalid={invalid || undefined}
        className={cn(
          'overflow-hidden rounded-lg border border-input bg-input/20 transition-shadow focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50 data-invalid:border-destructive data-invalid:ring-3 data-invalid:ring-destructive/20 dark:bg-input/10',
          className,
        )}
      />
    );
  },
);
