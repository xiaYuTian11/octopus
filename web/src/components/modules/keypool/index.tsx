'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
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
    startValidateChannelKeys,
    getValidateJobStatus,
    cancelValidateJob,
} from '@/api/endpoints/channel';
import { Loader2, RefreshCcw, Upload } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

const PAGE_SIZE = 50;
const CHUNK_SIZE = 2000;
const MAX_FILE_SIZE = 50 * 1024 * 1024; // 50MB

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
    const [isFileImporting, setIsFileImporting] = useState(false);
    const [validateModel, setValidateModel] = useState('gpt-4.1-nano');
    const [validateSummary, setValidateSummary] = useState<string>('');
    const [importStatus, setImportStatus] = useState<{
        active: boolean;
        currentBatch: number;
        totalBatches: number;
        added: number;
        skipped: number;
        dedupSkipped: number;
    }>({ active: false, currentBatch: 0, totalBatches: 0, added: 0, skipped: 0, dedupSkipped: 0 });
    const [validateStart, setValidateStart] = useState<number | null>(null);
    const [validateElapsed, setValidateElapsed] = useState<number>(0);
    const [validateJobId, setValidateJobId] = useState<string | null>(null);
    type ValidateState = 'idle' | 'pending' | 'running' | 'success' | 'error' | 'canceled';
    const [validateProgress, setValidateProgress] = useState<{
        state: ValidateState;
        tested: number;
        success: number;
        disabled: number;
        total: number;
        error?: string;
    }>({ state: 'idle', tested: 0, success: 0, disabled: 0, total: 0 });
    const validatePollRef = useRef<NodeJS.Timeout | null>(null);
    const fileInputRef = useRef<HTMLInputElement | null>(null);

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

    useEffect(() => {
        if (validateStart === null) {
            setValidateElapsed(0);
            return;
        }
        setValidateElapsed(0);
        const timer = setInterval(() => {
            setValidateElapsed(Math.floor((Date.now() - validateStart) / 1000));
        }, 1000);
        return () => clearInterval(timer);
    }, [validateStart]);

    useEffect(() => {
        return () => {
            if (validatePollRef.current) {
                clearInterval(validatePollRef.current);
            }
        };
    }, []);

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




    const handleFileImportClick = () => {
        fileInputRef.current?.click();
    };

    const runImport = async (rawText: string) => {
        if (!selectedChannelId) {
            toast.error('请选择渠道后再导入');
            return;
        }
        const lines = rawText
            .split(/\r?\n/)
            .map((l) => l.trim())
            .filter(Boolean);
        if (lines.length === 0) {
            toast.error('文件内容为空');
            return;
        }

        const uniqueLines = Array.from(new Set(lines));
        const dedupSkipped = lines.length - uniqueLines.length;

        const totalBatches = Math.ceil(uniqueLines.length / CHUNK_SIZE);
        setImportStatus({ active: true, currentBatch: 0, totalBatches, added: 0, skipped: 0, dedupSkipped });

        let added = 0;
        let skipped = 0;
        try {
            for (let i = 0; i < totalBatches; i++) {
                const chunk = uniqueLines.slice(i * CHUNK_SIZE, (i + 1) * CHUNK_SIZE).join('\n');
                const res = await importKeys.mutateAsync({ channel_id: selectedChannelId!, text: chunk });
                added += res.added ?? 0;
                skipped += res.skipped ?? 0;
                setImportStatus({ active: true, currentBatch: i + 1, totalBatches, added, skipped, dedupSkipped });
            }
            const msg = t('importSuccess', { added, skipped: skipped + dedupSkipped });
            toast.success(msg);
            setLastMessage(msg);
            setText('');
            keyPage.refetch();
            setPage(1);
        } catch (error) {
            const message = error instanceof Error ? error.message : 'Import failed';
            toast.error(message);
        } finally {
            setImportStatus({ active: false, currentBatch: 0, totalBatches: 0, added: 0, skipped: 0, dedupSkipped: 0 });
        }
    };

    const handleFileImport = async (file: File) => {
        if (!selectedChannelId) {
            toast.error('请选择渠道后再导入');
            return;
        }
        if (file.size > MAX_FILE_SIZE) {
            toast.error('文件过大，最大支持 50MB 的 TXT');
            return;
        }
        setIsFileImporting(true);
        try {
            const content = await file.text();
            await runImport(content);
        } catch (error) {
            const message = error instanceof Error ? error.message : 'Import failed';
            toast.error(message);
        } finally {
            setIsFileImporting(false);
        }
    };

    const handleFileInputChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
        const file = e.target.files?.[0];
        // 重置 value 以便选择同一文件也能触发
        e.target.value = '';
        if (!file) return;
        await handleFileImport(file);
    };

    const handleImport = () => {
        if (!selectedChannelId || !text.trim()) return;
        runImport(text);
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

    const stopValidatePolling = () => {
        if (validatePollRef.current) {
            clearInterval(validatePollRef.current);
            validatePollRef.current = null;
        }
    };

    const handleValidate = () => {
        if (!selectedChannelId) {
            toast.error('请先选择渠道');
            return;
        }
        const model = validateModel.trim();
        if (!model) {
            toast.error('请输入要测试的模型');
            return;
        }
        setValidateSummary('');
        setValidateStart(Date.now());
        setValidateProgress({ state: 'running', tested: 0, success: 0, disabled: 0, total: 0 });

        startValidateChannelKeys({ channel_id: selectedChannelId, model, timeout: 15, concurrency: 2 })
            .then((res) => {
                const { job_id, total } = res;
                setValidateJobId(job_id);
                setValidateProgress((prev) => ({ ...prev, total, state: 'running' }));

                validatePollRef.current = setInterval(async () => {
                    try {
                        const status = await getValidateJobStatus(job_id);
                        setValidateProgress({
                            state: (status.state as ValidateState) || 'pending',
                            tested: status.tested,
                            success: status.success,
                            disabled: status.disabled,
                            total: status.total,
                            error: status.error,
                        });
                        if (status.state === 'success' || status.state === 'error' || status.state === 'canceled') {
                            stopValidatePolling();
                            setValidateJobId(null);
                            const msg =
                                status.state === 'success'
                                    ? `测试完成：总计 ${status.tested}/${status.total}，成功 ${status.success}，禁用 ${status.disabled}`
                                    : status.state === 'canceled'
                                    ? '已取消测试'
                                    : `测试失败：${status.error || '未知错误'}`;
                            setValidateSummary(msg);
                            toast[status.state === 'success' ? 'success' : 'error'](msg);
                            setLastMessage(msg);
                            keyPage.refetch();
                            setValidateStart(null);
                            setValidateElapsed(0);
                        }
                    } catch (err) {
                        stopValidatePolling();
                        setValidateJobId(null);
                        setValidateProgress((prev) => ({ ...prev, state: 'error', error: (err as Error)?.message }));
                        toast.error((err as Error)?.message || '验证失败');
                        setValidateStart(null);
                        setValidateElapsed(0);
                    }
                }, 3000);
            })
            .catch((error) => {
                setValidateStart(null);
                setValidateElapsed(0);
                const message = error instanceof Error ? error.message : '验证失败';
                toast.error(message);
            });
    };

    const handleCancelValidate = () => {
        if (!validateJobId) return;
        stopValidatePolling();
        cancelValidateJob(validateJobId)
            .then(() => {
                setValidateProgress((prev) => ({ ...prev, state: 'canceled' }));
                setValidateJobId(null);
                setValidateStart(null);
                setValidateElapsed(0);
            })
            .catch((err) => {
                toast.error((err as Error)?.message || '取消失败');
            });
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
                            {t('enabled')}: {keyEnabled} / {t('disabled')}: {keyDisabled}
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
                        <input
                            ref={fileInputRef}
                            type="file"
                            accept=".txt"
                            className="hidden"
                            onChange={handleFileInputChange}
                        />
                        <div className="flex flex-wrap gap-2">
                            <Button onClick={handleImport} disabled={importKeys.isPending || !text.trim() || importStatus.active} className="rounded-xl">
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
                                variant="outline"
                                onClick={handleFileImportClick}
                                disabled={isFileImporting || importKeys.isPending || importStatus.active}
                                className="rounded-xl"
                            >
                                {isFileImporting ? (
                                    <span className="flex items-center gap-2">
                                        <Loader2 className="h-4 w-4 animate-spin" />
                                        {tActions('saving')}
                                    </span>
                                ) : (
                                    <span className="flex items-center gap-2">
                                        <Upload className="h-4 w-4" />
                                        {t('import')} TXT
                                    </span>
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
                        <div className="flex flex-wrap items-end gap-3">
                            <div className="space-y-1">
                                <label className="text-xs text-muted-foreground">测试模型</label>
                                <Input
                                    className="rounded-xl w-56"
                                    value={validateModel}
                                    onChange={(e) => setValidateModel(e.target.value)}
                                    placeholder="如 gpt-4.1-nano"
                                    disabled={!!validateJobId}
                                />
                            </div>
                            <Button
                                type="button"
                                onClick={handleValidate}
                                disabled={!!validateJobId || importStatus.active || isFileImporting}
                                className="rounded-xl"
                            >
                                {validateJobId ? (
                                    <span className="flex items-center gap-2">
                                        <Loader2 className="h-4 w-4 animate-spin" />
                                        测试中...
                                    </span>
                                ) : (
                                    '一键测试'
                                )}
                            </Button>
                            {validateJobId && (
                                <Button variant="outline" type="button" onClick={handleCancelValidate} className="rounded-xl">
                                    取消
                                </Button>
                            )}
                            {validateSummary && !validateJobId && (
                                <div className="text-xs text-muted-foreground">{validateSummary}</div>
                            )}
                        </div>
                        {validateProgress.state !== 'idle' && (
                            <div className="text-sm text-muted-foreground">
                                进度：{validateProgress.tested}/{validateProgress.total || '?'}，成功 {validateProgress.success}，禁用 {validateProgress.disabled}
                                {validateProgress.state === 'error' && validateProgress.error ? `，错误：${validateProgress.error}` : ''}
                            </div>
                        )}
                        {(validateJobId ||
                            importKeys.isPending ||
                            restoreKeys.isPending ||
                            clearKeys.isPending ||
                            importStatus.active) && (
                            <div className="text-sm text-muted-foreground flex items-center gap-2">
                                <Loader2 className="h-4 w-4 animate-spin" />
                                {validateJobId
                                    ? `测试中${validateElapsed > 0 ? `，已用时 ${validateElapsed}s` : ''}${
                                          validateProgress.total > 0 ? `，进度 ${validateProgress.tested}/${validateProgress.total}` : ''
                                      }，请勿关闭页面`
                                    : importStatus.active && importStatus.totalBatches > 0
                                    ? `导入中 ${importStatus.currentBatch}/${importStatus.totalBatches}，新增 ${importStatus.added}，跳过 ${importStatus.skipped}，去重 ${importStatus.dedupSkipped}`
                                    : tActions('saving')}
                            </div>
                        )}
                        {lastMessage &&
                            !validateJobId &&
                            !importKeys.isPending &&
                            !restoreKeys.isPending &&
                            !clearKeys.isPending &&
                            !importStatus.active && (
                            <div className="text-sm text-muted-foreground">{lastMessage}</div>
                        )}
                    </div>

                    <div className="border rounded-xl p-3 space-y-3">
                        <div className="flex items-center justify-between">
                            <div className="text-sm font-medium text-card-foreground">{t('apiKey')}</div>
                            <div className="text-xs text-muted-foreground">
                                {t('enabled')}: {keyEnabled} / {t('disabled')}: {keyDisabled} / 总计 {total}
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
