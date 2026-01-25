'use client';

import { useEffect, useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';
import { toast } from '@/components/common/Toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Card } from '@/components/ui/card';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { useChannelList, useImportChannelKeys, useRestoreInvalidChannelKeys, useClearInvalidChannelKeys } from '@/api/endpoints/channel';
import { Loader2 } from 'lucide-react';

export function KeyPool() {
    const { data: channels } = useChannelList();
    const poolChannels = useMemo(() => (channels ?? []).filter((c) => c.raw.key_pool_enabled), [channels]);
    const t = useTranslations('channel.form');
    const tNav = useTranslations('navbar');
    const tActions = useTranslations('channel.detail.actions');

    const [selectedChannelId, setSelectedChannelId] = useState<number | null>(null);
    const [text, setText] = useState('');
    const [lastMessage, setLastMessage] = useState<string>('');
    const importKeys = useImportChannelKeys();
    const restoreKeys = useRestoreInvalidChannelKeys();
    const clearKeys = useClearInvalidChannelKeys();

    useEffect(() => {
        if (selectedChannelId === null && poolChannels.length > 0) {
            setSelectedChannelId(poolChannels[0].raw.id);
        }
    }, [poolChannels, selectedChannelId]);

    const selectedChannel = useMemo(
        () => poolChannels.find((c) => c.raw.id === selectedChannelId),
        [poolChannels, selectedChannelId]
    );

    const handleImport = () => {
        if (!selectedChannelId || !text.trim()) return;
        importKeys.mutate(
            { channel_id: selectedChannelId, text },
            {
                onSuccess: (res) => {
                    const msg = t('importSuccess', { added: res.added ?? 0, skipped: res.skipped ?? 0 });
                    toast.success(msg);
                    setLastMessage(msg);
                    setText('');
                },
                onError: (error) => toast.error(error.message ?? 'Import failed'),
            }
        );
    };

    const handleRestore = () => {
        if (!selectedChannelId) return;
        restoreKeys.mutate(
            { channel_id: selectedChannelId },
            {
                onSuccess: (res) => {
                    const msg = t('restoreSuccess', { count: res.restored ?? 0 });
                    toast.success(msg);
                    setLastMessage(msg);
                },
                onError: (error) => toast.error(error.message ?? 'Restore failed'),
            }
        );
    };

    const handleClear = () => {
        if (!selectedChannelId) return;
        clearKeys.mutate(
            { channel_id: selectedChannelId },
            {
                onSuccess: (res) => {
                    const msg = t('clearSuccess', { count: res.cleared ?? 0 });
                    toast.success(msg);
                    setLastMessage(msg);
                },
                onError: (error) => toast.error(error.message ?? 'Clear failed'),
            }
        );
    };

    if (!poolChannels || poolChannels.length === 0) {
        return (
            <div className="p-6 md:p-10">
                <Card className="p-6">
                    <div className="text-lg font-semibold mb-2">{tNav('keypool')}</div>
                    <div className="text-sm text-muted-foreground">{t('keyPoolDesc')}</div>
                    <div className="text-sm text-muted-foreground mt-2">{t('keyPool')}</div>
                </Card>
            </div>
        );
    }

    return (
        <div className="p-6 md:p-10 space-y-6">
            <div className="flex flex-col gap-2">
                <div className="text-2xl font-bold">{tNav('keypool')}</div>
                <div className="text-sm text-muted-foreground">{t('keyPoolDesc')}</div>
            </div>

            <Card className="p-6 space-y-4">
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                    <div className="space-y-2">
                        <label className="text-sm font-medium text-card-foreground">{t('name')}</label>
                        <Select
                            value={selectedChannelId ? String(selectedChannelId) : undefined}
                            onValueChange={(val) => setSelectedChannelId(Number(val))}
                        >
                            <SelectTrigger className="rounded-xl">
                                <SelectValue placeholder={t('name')} />
                            </SelectTrigger>
                            <SelectContent>
                                {poolChannels.map((c) => (
                                    <SelectItem key={c.raw.id} value={String(c.raw.id)}>
                                        {c.raw.name}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                    </div>
                    <div className="space-y-2">
                        <label className="text-sm font-medium text-card-foreground">{t('keyFailThreshold')}</label>
                        <Input
                            className="rounded-xl"
                            value={selectedChannel?.raw.key_fail_threshold ?? 3}
                            disabled
                        />
                    </div>
                    <div className="space-y-2">
                        <label className="text-sm font-medium text-card-foreground">{t('apiKey')}</label>
                        <Input
                            className="rounded-xl"
                            value={selectedChannel?.raw.keys?.length ?? 0}
                            disabled
                        />
                    </div>
                </div>

                <div className="space-y-2">
                    <label className="text-sm font-medium text-card-foreground">{t('keyPoolTools')}</label>
                    <textarea
                        className="min-h-[160px] w-full rounded-xl border border-border bg-background p-3 text-sm"
                        placeholder={t('keyPoolPaste')}
                        value={text}
                        onChange={(e) => setText(e.target.value)}
                    />
                </div>

                <div className="flex flex-wrap gap-2">
                    <Button
                        onClick={handleImport}
                        disabled={importKeys.isPending || !text.trim()}
                        className="rounded-xl"
                    >
                        {importKeys.isPending ? (
                            <span className="flex items-center gap-2">
                                <Loader2 className="h-4 w-4 animate-spin" />
                                {tActions('saving')}
                            </span>
                        ) : (
                            t('import')
                        )}
                    </Button>
                    <Button
                        variant="secondary"
                        onClick={handleRestore}
                        disabled={restoreKeys.isPending}
                        className="rounded-xl"
                    >
                        {restoreKeys.isPending ? (
                            <span className="flex items-center gap-2">
                                <Loader2 className="h-4 w-4 animate-spin" />
                                {tActions('saving')}
                            </span>
                        ) : (
                            t('restoreInvalid')
                        )}
                    </Button>
                    <Button
                        variant="outline"
                        onClick={handleClear}
                        disabled={clearKeys.isPending}
                        className="rounded-xl"
                    >
                        {clearKeys.isPending ? (
                            <span className="flex items-center gap-2">
                                <Loader2 className="h-4 w-4 animate-spin" />
                                {tActions('saving')}
                            </span>
                        ) : (
                            t('clearInvalid')
                        )}
                    </Button>
                </div>

                {(importKeys.isPending || restoreKeys.isPending || clearKeys.isPending) && (
                    <div className="text-sm text-muted-foreground flex items-center gap-2">
                        <Loader2 className="h-4 w-4 animate-spin" />
                        {tActions('saving')}
                    </div>
                )}
                {lastMessage && !importKeys.isPending && !restoreKeys.isPending && !clearKeys.isPending && (
                    <div className="text-sm text-muted-foreground">{lastMessage}</div>
                )}
            </Card>
        </div>
    );
}
