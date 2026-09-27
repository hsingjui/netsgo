import { useCallback, useMemo, useState, type ReactNode } from 'react';

import { AddClientDialog } from './AddClientDialog';
import { AddClientDialogContext } from './add-client-dialog-context';
import type { ResourceScope } from '@/lib/resource-scope';

export function AddClientDialogProvider({
  scope,
  children,
}: {
  scope: ResourceScope | null;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const openAddClientDialog = useCallback(() => setOpen(true), []);
  const value = useMemo(() => ({ openAddClientDialog }), [openAddClientDialog]);

  return (
    <AddClientDialogContext.Provider value={value}>
      {children}
      {scope ? <AddClientDialog scope={scope} open={open} onOpenChange={setOpen} /> : null}
    </AddClientDialogContext.Provider>
  );
}
