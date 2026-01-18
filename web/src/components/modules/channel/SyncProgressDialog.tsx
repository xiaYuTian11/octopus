'use client';

import { useTranslations } from 'next-intl';
import { Loader2, CheckCircle, XCircle, AlertCircle } from 'lucide-react';
import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogFooter,
} from '@/components/ui/dialog';
import { Progress } from '@/components/ui/progress';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

/**
 * 同步结果类型
 */
export interface SyncResult {
    channelId: number;
    channelName: string;
    status: 'pending' | 'syncing' | 'success' | 'error';
    error?: string;
}

interface SyncProgressDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    results: SyncResult[];
    isCompleted: boolean;
}

/**
 * 同步进度对话框组件
 */
export function SyncProgressDialog({
    open,
    onOpenChange,
    results,
    isCompleted,
}: SyncProgressDialogProps) {
    const t = useTranslations('channel.syncProgress');

    // 计算统计数据
    const total = results.length;
    const successCount = results.filter((r) => r.status === 'success').length;
    const errorCount = results.filter((r) => r.status === 'error').length;
    const syncingCount = results.filter((r) => r.status === 'syncing').length;
    const completedCount = successCount + errorCount;
    const progress = total > 0 ? (completedCount / total) * 100 : 0;

    // 获取状态图标
    const getStatusIcon = (status: SyncResult['status']) => {
        switch (status) {
            case 'syncing':
                return <Loader2 className="size-4 animate-spin text-blue-500" />;
            case 'success':
                return <CheckCircle className="size-4 text-green-500" />;
            case 'error':
                return <XCircle className="size-4 text-red-500" />;
            default:
                return <AlertCircle className="size-4 text-muted-foreground" />;
        }
    };

    // 获取状态文本
    const getStatusText = (status: SyncResult['status']) => {
        switch (status) {
            case 'syncing':
                return t('statusSyncing');
            case 'success':
                return t('statusSuccess');
            case 'error':
                return t('statusError');
            default:
                return t('statusPending');
        }
    };

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent className="sm:max-w-md" showCloseButton={false}>
                <DialogHeader>
                    <DialogTitle>{t('title')}</DialogTitle>
                </DialogHeader>

                <div className="space-y-4">
                    {/* 进度条 */}
                    <div className="space-y-2">
                        <div className="flex items-center justify-between text-sm">
                            <span className="text-muted-foreground">
                                {t('completed', { current: completedCount, total })}
                            </span>
                            <span className="font-medium">{Math.round(progress)}%</span>
                        </div>
                        <Progress value={progress} className="h-2" />
                    </div>

                    {/* 统计信息 */}
                    <div className="flex items-center justify-center gap-4 text-sm">
                        <div className="flex items-center gap-1.5">
                            <CheckCircle className="size-4 text-green-500" />
                            <span>{t('successCount', { count: successCount })}</span>
                        </div>
                        <div className="flex items-center gap-1.5">
                            <XCircle className="size-4 text-red-500" />
                            <span>{t('errorCount', { count: errorCount })}</span>
                        </div>
                        {syncingCount > 0 && (
                            <div className="flex items-center gap-1.5">
                                <Loader2 className="size-4 animate-spin text-blue-500" />
                                <span>{t('syncingCount', { count: syncingCount })}</span>
                            </div>
                        )}
                    </div>

                    {/* 渠道列表 */}
                    <div className="max-h-60 overflow-y-auto rounded-md border">
                        <div className="divide-y">
                            {results.map((result) => (
                                <div
                                    key={result.channelId}
                                    className={cn(
                                        'flex items-start gap-3 px-3 py-2',
                                        result.status === 'syncing' && 'bg-blue-50 dark:bg-blue-950/20',
                                        result.status === 'error' && 'bg-red-50 dark:bg-red-950/20'
                                    )}
                                >
                                    <div className="mt-0.5 shrink-0">
                                        {getStatusIcon(result.status)}
                                    </div>
                                    <div className="min-w-0 flex-1">
                                        <div className="flex items-center justify-between gap-2">
                                            <span className="truncate font-medium text-sm">
                                                {result.channelName}
                                            </span>
                                            <span
                                                className={cn(
                                                    'shrink-0 text-xs',
                                                    result.status === 'success' && 'text-green-600',
                                                    result.status === 'error' && 'text-red-600',
                                                    result.status === 'syncing' && 'text-blue-600',
                                                    result.status === 'pending' && 'text-muted-foreground'
                                                )}
                                            >
                                                {getStatusText(result.status)}
                                            </span>
                                        </div>
                                        {result.error && (
                                            <p className="mt-1 text-xs text-red-600 dark:text-red-400">
                                                {result.error}
                                            </p>
                                        )}
                                    </div>
                                </div>
                            ))}
                        </div>
                    </div>
                </div>

                <DialogFooter>
                    <Button
                        onClick={() => onOpenChange(false)}
                        disabled={!isCompleted}
                        className="w-full sm:w-auto"
                    >
                        {isCompleted ? t('close') : t('syncing')}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
