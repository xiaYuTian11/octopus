'use client';

import { useEffect, useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';
import { toast } from '@/components/common/Toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Card } from '@/components/ui/card';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import {
    useChannelList,
    useImportChannelKeys,
    useRestoreInvalidChannelKeys,
    useClearInvalidChannelKeys,
    useChannelKeys,
} from '@/api/endpoints/channel';
import { Loader2, RefreshCcw } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

const PAGE_SIZE = 50;

export function KeyPool() {
    const { data: channels } = useChannelList();
    const poolChannels = useMemo(() => (channels ?? []).filter((c) => c.raw.key_pool_enabled), [channels]);
    const t = useTranslations('channel.form');
    const tNav = useTranslations('navbar');
    const tActions = useTranslations('channel.detail.actions');

    const [selectedChannelId, setSelectedChannelId] = useState<number | null>(null);
    const [text, setText] = useState('');
    const [lastMessage, setLastMessage] = useState<string>('');
    const [page, setPage] = useState(1);
    const [enabledFilter, setEnabledFilter] = useState<'all' | 'enabled' | 'disabled'>('all');

    const importKeys = useImportChannelKeys();
    const restoreKeys = useRestoreInvalidChannelKeys();
    const clearKeys = useClearInvalidChannelKeys();

    useEffect(() => {
        if (selectedChannelId === null && poolChannels.length > 0) {
            setSelectedChannelId(poolChannels[0].raw.id);
        }
    }, [poolChannels, selectedChannelId]);

    useEffect(() => {
        setPage(1);
    }, [selectedChannelId, enabledFilter]);

    const selectedChannel = useMemo(
        () => poolChannels.find((c) => c.raw.id === selectedChannelId),
        [poolChannels, selectedChannelId]
    );

    const keyTotal = selectedChannel?.raw.key_count ?? 0;
    const keyEnabled = selectedChannel?.raw.key_enabled_count ?? 0;
    const keyDisabled = selectedChannel?.raw.key_disabled_count ?? Math.max(keyTotal - keyEnabled, 0);

    const enabledParam = enabledFilter === 'all' ? undefined : enabledFilter === 'enabled';
    const keyPage = useChannelKeys({ channel_id: selectedChannelId ?? undefined, page, page_size: PAGE_SIZE, enabled: enabledParam });
    const items = keyPage.data?.items ?? [];
    const total = keyPage.data?.total ?? 0;
    const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

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
                    keyPage.refetch();
                    setPage(1);
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
                    keyPage.refetch();
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
                    keyPage.refetch();
                    setPage(1);
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
                        <Input className="rounded-xl" value={selectedChannel?.raw.key_fail_threshold ?? 3} disabled />
                    </div>
                    <div className="space-y-2">
                        <label className="text-sm font-medium text-card-foreground">{t('apiKey')}</label>
                        <Input className="rounded-xl" value={keyTotal} disabled />
                        <div className="text-xs text-muted-foreground">
                            {t('enabled')}: {keyEnabled} · {t('disabled')}: {keyDisabled}
                        </div>
                    </div>
                </div>

                <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                    <div className="space-y-3">
                        <label className="text-sm font-medium text-card-foreground">{t('keyPoolTools')}</label>
                        <textarea
                            className="min-h-[160px] w-full rounded-xl border border-border bg-background p-3 text-sm"
                            placeholder={t('keyPoolPaste')}
                            value={text}
                            onChange={(e) => setText(e.target.value)}
                        />
                        <div className="flex flex-wrap gap-2">
                            <Button onClick={handleImport} disabled={importKeys.isPending || !text.trim()} className="rounded-xl">
                                {importKeys.isPending ? (
                                    <span className="flex items-center gap-2">
                                        <Loader2 className="h-4 w-4 animate-spin" />
                                        {tActions('saving')}
                                    </span>
                                ) : (
                                    t('import')
                                )}
                            </Button>
                            <Button variant="secondary" onClick={handleRestore} disabled={restoreKeys.isPending} className="rounded-xl">
                                {restoreKeys.isPending ? (
                                    <span className="flex items-center gap-2">
                                        <Loader2 className="h-4 w-4 animate-spin" />
                                        {tActions('saving')}
                                    </span>
                                ) : (
                                    t('restoreInvalid')
                                )}
                            </Button>
                            <Button variant="outline" onClick={handleClear} disabled={clearKeys.isPending} className="rounded-xl">
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
                    </div>

                    <div className="border rounded-xl p-3 space-y-3">
                        <div className="flex items-center justify-between">
                            <div className="text-sm font-medium text-card-foreground">{t('apiKey')}</div>
                            <div className="text-xs text-muted-foreground">
                                {t('enabled')}: {keyEnabled} · {t('disabled')}: {keyDisabled} · {t('common.pagination.total', { count: total })}
                            </div>
                        </div>
                        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                            <span>{t('enabled')} / {t('disabled')}:</span>
                            <Button
                                type="button"
                                size="sm"
                                variant={enabledFilter === 'all' ? 'default' : 'outline'}
                                className="h-7 px-2 rounded-xl"
                                onClick={() => setEnabledFilter('all')}
                            >
                                All
                            </Button>
                            <Button
                                type="button"
                                size="sm"
                                variant={enabledFilter === 'enabled' ? 'default' : 'outline'}
                                className="h-7 px-2 rounded-xl"
                                onClick={() => setEnabledFilter('enabled')}
                            >
                                {t('enabled')}
                            </Button>
                            <Button
                                type="button"
                                size="sm"
                                variant={enabledFilter === 'disabled' ? 'default' : 'outline'}
                                className="h-7 px-2 rounded-xl"
                                onClick={() => setEnabledFilter('disabled')}
                            >
                                {t('disabled')}
                            </Button>
                            <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                className="h-7 px-2 rounded-xl"
                                onClick={() => keyPage.refetch()}
                                disabled={keyPage.isFetching}
                            >
                                <RefreshCcw className={cn('h-3.5 w-3.5 mr-1', keyPage.isFetching && 'animate-spin')} />
                                Refresh
                            </Button>
                        </div>
                        <div className="overflow-x-auto">
                            <table className="min-w-full text-sm">
                                <thead className="text-muted-foreground">
                                    <tr>
                                        <th className="text-left py-2 pr-3">Key</th>
                                        <th className="text-left py-2 pr-3">{t('enabled')}</th>
                                        <th className="text-left py-2 pr-3">Fail</th>
                                        <th className="text-left py-2 pr-3">Status</th>
                                        <th className="text-left py-2 pr-3">Last Use</th>
                                    </tr>
                                </thead>
                                <tbody className="divide-y">
                                    {items.map((k) => (
                                        <tr key={k.id}>
                                            <td className="py-2 pr-3 font-mono text-xs truncate max-w-[240px]">{k.channel_key}</td>
                                            <td className="py-2 pr-3">
                                                <Badge variant={k.enabled ? 'default' : 'secondary'} className="rounded-full">
                                                    {k.enabled ? t('enabled') : t('disabled')}
                                                </Badge>
                                            </td>
                                            <td className="py-2 pr-3">{k.failure_count ?? 0}</td>
                                            <td className="py-2 pr-3">
                                                {k.disabled_reason ? (
                                                    <span className="text-red-500 text-xs">{k.disabled_reason}</span>
                                                ) : (
                                                    <span className="text-muted-foreground text-xs">{k.status_code || '-'}</span>
                                                )}
                                            </td>
                                            <td className="py-2 pr-3 text-muted-foreground text-xs">
                                                {k.last_use_time_stamp ? new Date(k.last_use_time_stamp * 1000).toLocaleString() : '-'}
                                            </td>
                                        </tr>
                                    ))}
                                    {items.length === 0 && (
                                        <tr>
                                            <td className="py-3 text-center text-muted-foreground text-xs" colSpan={5}>
                                                {keyPage.isLoading ? tActions('saving') : t('noBaseUrls')}
                                            </td>
                                        </tr>
                                    )}
                                </tbody>
                            </table>
                        </div>
                        {totalPages > 1 && (
                            <div className="flex items-center justify-between text-xs text-muted-foreground">
                                <div>
                                    {page}/{totalPages}
                                </div>
                                <div className="flex gap-2">
                                    <Button
                                        type="button"
                                        size="sm"
                                        variant="ghost"
                                        disabled={page <= 1}
                                        onClick={() => setPage((p) => Math.max(1, p - 1))}
                                        className="rounded-xl h-7 px-2"
                                    >
                                        {'<'} Prev
                                    </Button>
                                    <Button
                                        type="button"
                                        size="sm"
                                        variant="ghost"
                                        disabled={page >= totalPages}
                                        onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                                        className="rounded-xl h-7 px-2"
                                    >
                                        Next {'>'}
                                    </Button>
                                </div>
                            </div>
                        )}
                    </div>
                </div>
            </Card>
        </div>
    );
}
