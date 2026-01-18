'use client';

import { useState, useCallback, useEffect, useRef } from 'react';
import { CheckCircle2, XCircle, Loader2 } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogFooter,
} from '@/components/ui/dialog';
import { Progress } from '@/components/ui/progress';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { apiClient } from '@/api/client';
import type { TestChannelResponse } from '@/api/endpoints/channel';

export interface TestTarget {
    channelId: number;
    channelName: string;
    model: string;
}

export interface TestResult {
    channelId: number;
    model: string;
    status: 'pending' | 'testing' | 'success' | 'error';
    latency?: number;
    error?: string;
}

interface BatchTestDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    targets: TestTarget[];
    title?: string;
}

const MAX_CONCURRENT = 5;

export function BatchTestDialog({ open, onOpenChange, targets, title }: BatchTestDialogProps) {
    const t = useTranslations('test');
    const [results, setResults] = useState<Map<string, TestResult>>(new Map());
    const [isTesting, setIsTesting] = useState(false);
    const abortControllerRef = useRef<AbortController | null>(null);

    const getKey = (channelId: number, model: string) => `${channelId}-${model}`;

    const runTests = useCallback(async () => {
        if (targets.length === 0) return;

        setIsTesting(true);
        abortControllerRef.current = new AbortController();

        const initialResults = new Map<string, TestResult>();
        targets.forEach(({ channelId, model }) => {
            initialResults.set(getKey(channelId, model), {
                channelId,
                model,
                status: 'pending',
            });
        });
        setResults(initialResults);

        const queue = [...targets];
        const runningPromises: Promise<void>[] = [];

        const runSingle = async (target: TestTarget) => {
            const key = getKey(target.channelId, target.model);

            setResults(prev => {
                const next = new Map(prev);
                next.set(key, { ...prev.get(key)!, status: 'testing' });
                return next;
            });

            try {
                const response = await apiClient.post<TestChannelResponse>(
                    '/api/v1/channel/test',
                    { channel_id: target.channelId, model: target.model }
                );

                setResults(prev => {
                    const next = new Map(prev);
                    next.set(key, {
                        channelId: target.channelId,
                        model: target.model,
                        status: response.success ? 'success' : 'error',
                        latency: response.latency,
                        error: response.error,
                    });
                    return next;
                });
            } catch (err) {
                setResults(prev => {
                    const next = new Map(prev);
                    next.set(key, {
                        channelId: target.channelId,
                        model: target.model,
                        status: 'error',
                        error: err instanceof Error ? err.message : 'Unknown error',
                    });
                    return next;
                });
            }
        };

        while (queue.length > 0 || runningPromises.length > 0) {
            if (abortControllerRef.current?.signal.aborted) break;

            while (runningPromises.length < MAX_CONCURRENT && queue.length > 0) {
                const target = queue.shift()!;
                const promise = runSingle(target).then(() => {
                    const index = runningPromises.indexOf(promise);
                    if (index > -1) runningPromises.splice(index, 1);
                });
                runningPromises.push(promise);
            }

            if (runningPromises.length > 0) {
                await Promise.race(runningPromises);
            }
        }

        setIsTesting(false);
    }, [targets]);

    useEffect(() => {
        if (open && targets.length > 0) {
            runTests();
        }
        return () => {
            abortControllerRef.current?.abort();
        };
    }, [open, targets, runTests]);

    const handleClose = () => {
        abortControllerRef.current?.abort();
        setIsTesting(false);
        onOpenChange(false);
    };

    const completedCount = Array.from(results.values()).filter(
        r => r.status === 'success' || r.status === 'error'
    ).length;
    const successCount = Array.from(results.values()).filter(r => r.status === 'success').length;
    const errorCount = Array.from(results.values()).filter(r => r.status === 'error').length;
    const progress = targets.length > 0 ? (completedCount / targets.length) * 100 : 0;

    return (
        <Dialog open={open} onOpenChange={handleClose}>
            <DialogContent className="sm:max-w-lg">
                <DialogHeader>
                    <DialogTitle>{title || t('title')}</DialogTitle>
                </DialogHeader>

                <div className="space-y-4">
                    <div className="flex items-center justify-between text-sm text-muted-foreground">
                        <span>{t('progress')}</span>
                        <span>{completedCount} / {targets.length}</span>
                    </div>
                    <Progress value={progress} className="h-2" />

                    <div className="max-h-[300px] overflow-y-auto space-y-2 pr-1">
                        {targets.map(({ channelId, channelName, model }) => {
                            const key = getKey(channelId, model);
                            const result = results.get(key);
                            const status = result?.status || 'pending';

                            return (
                                <div
                                    key={key}
                                    className={cn(
                                        'flex items-center justify-between p-3 rounded-xl border transition-colors',
                                        status === 'success' && 'border-green-500/30 bg-green-500/5',
                                        status === 'error' && 'border-red-500/30 bg-red-500/5',
                                        (status === 'pending' || status === 'testing') && 'border-border bg-muted/30'
                                    )}
                                >
                                    <div className="flex items-center gap-3 min-w-0">
                                        {status === 'pending' && (
                                            <div className="size-5 rounded-full border-2 border-muted-foreground/30" />
                                        )}
                                        {status === 'testing' && (
                                            <Loader2 className="size-5 text-primary animate-spin" />
                                        )}
                                        {status === 'success' && (
                                            <CheckCircle2 className="size-5 text-green-500" />
                                        )}
                                        {status === 'error' && (
                                            <XCircle className="size-5 text-red-500" />
                                        )}
                                        <div className="min-w-0">
                                            <div className="text-sm font-medium truncate">{channelName}</div>
                                            <div className="text-xs text-muted-foreground truncate">{model}</div>
                                        </div>
                                    </div>

                                    <div className="flex items-center gap-2 shrink-0">
                                        {result?.latency !== undefined && (
                                            <Badge
                                                variant="secondary"
                                                className={cn(
                                                    'text-xs',
                                                    result.latency < 1000 && 'bg-green-500/15 text-green-700 dark:text-green-400',
                                                    result.latency >= 1000 && result.latency < 3000 && 'bg-yellow-500/15 text-yellow-700 dark:text-yellow-400',
                                                    result.latency >= 3000 && 'bg-red-500/15 text-red-700 dark:text-red-400'
                                                )}
                                            >
                                                {result.latency}ms
                                            </Badge>
                                        )}
                                        {status === 'error' && result?.error && (
                                            <span className="text-xs text-red-500 max-w-[120px] truncate" title={result.error}>
                                                {result.error}
                                            </span>
                                        )}
                                    </div>
                                </div>
                            );
                        })}
                    </div>

                    {!isTesting && completedCount > 0 && (
                        <div className="flex items-center justify-center gap-4 text-sm">
                            <span className="text-green-500">{t('success')}: {successCount}</span>
                            <span className="text-red-500">{t('failed')}: {errorCount}</span>
                        </div>
                    )}
                </div>

                <DialogFooter>
                    <Button variant="outline" onClick={handleClose}>
                        {isTesting ? t('cancel') : t('close')}
                    </Button>
                    {!isTesting && completedCount === targets.length && (
                        <Button onClick={runTests}>{t('retry')}</Button>
                    )}
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
