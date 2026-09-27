import { useMemo, useState } from 'react';
import { AlertTriangle, ArrowRightLeft } from 'lucide-react';
import toast from 'react-hot-toast';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { useMigrateTunnel } from '@/hooks/use-tunnel-mutations';
import { getClientDisplayName } from '@/lib/client-utils';
import { buildTunnelMigrationInput, getTunnelMigrationCandidates } from '@/lib/tunnel-migration';
import type { Client, ProxyConfig } from '@/types';
import type { ResourceScope } from '@/lib/resource-scope';

interface TunnelMigrateDialogProps {
  scope: ResourceScope;
  tunnel: ProxyConfig | null;
  clients?: Client[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function TunnelMigrateDialog({
  scope,
  tunnel,
  clients = [],
  open,
  onOpenChange,
}: TunnelMigrateDialogProps) {
  const { t } = useTranslation();
  const migrateTunnel = useMigrateTunnel(scope);
  const [selection, setSelection] = useState({ tunnelId: '', targetClientId: '' });
  const candidates = useMemo(
    () => getTunnelMigrationCandidates(tunnel, clients),
    [clients, tunnel],
  );
  const targetClientId = selection.tunnelId === tunnel?.id ? selection.targetClientId : '';
  const migrationInput = useMemo(
    () => buildTunnelMigrationInput(tunnel, targetClientId, clients),
    [clients, targetClientId, tunnel],
  );

  const setDialogOpen = (nextOpen: boolean) => {
    if (!nextOpen) {
      setSelection({ tunnelId: '', targetClientId: '' });
      migrateTunnel.reset();
    }
    onOpenChange(nextOpen);
  };

  const submitMigration = () => {
    if (!migrationInput || !tunnel) {
      return;
    }
    const tunnelName = tunnel.name;

    migrateTunnel.mutate(migrationInput, {
      onSuccess: () => {
        toast.success(t('tunnels.migrated', { name: tunnelName }));
        setDialogOpen(false);
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={setDialogOpen}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ArrowRightLeft className="h-5 w-5" />
            {t('tunnels.migrateTunnel')}
          </DialogTitle>
          <DialogDescription>
            {t('tunnels.migrateDescription', { name: tunnel?.name ?? '' })}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2 py-2">
          <label className="text-sm font-medium" htmlFor="tunnel-migrate-target">
            {t('tunnels.migrateTargetClient')}
          </label>
          {candidates.length > 0 ? (
            <Select
              value={targetClientId}
              onValueChange={(targetClientId) => setSelection({ tunnelId: tunnel?.id ?? '', targetClientId })}
            >
              <SelectTrigger id="tunnel-migrate-target" aria-label={t('tunnels.migrateTargetClient')} className="w-full">
                <SelectValue placeholder={t('tunnels.migrateSelectPlaceholder')} />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {candidates.map((client) => (
                    <SelectItem key={client.id} value={client.id}>
                      {getClientDisplayName(client)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          ) : (
            <p className="text-sm text-muted-foreground">{t('tunnels.migrateNoCandidates')}</p>
          )}
        </div>

        {migrateTunnel.isError && (
          <div className="flex items-center gap-2 rounded border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            {(migrateTunnel.error as Error).message}
          </div>
        )}

        <DialogFooter>
          <Button type="button" variant="outline" disabled={migrateTunnel.isPending} onClick={() => setDialogOpen(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            type="button"
            onClick={submitMigration}
            disabled={!migrationInput || migrateTunnel.isPending}
          >
            {migrateTunnel.isPending ? t('tunnels.migrating') : t('tunnels.migrateSubmit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
