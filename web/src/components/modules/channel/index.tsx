'use client';

import { useEffect, useMemo, useState } from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { RefreshCw } from 'lucide-react';
import { useChannelList, useSyncChannel } from '@/api/endpoints/channel';
import { Card } from './Card';
import { usePaginationStore, useSearchStore } from '@/components/modules/toolbar';
import { EASING } from '@/lib/animations/fluid-transitions';
import { useGridPageSize } from '@/hooks/use-grid-page-size';
import { Button } from '@/components/ui/button';
import { toast } from '@/components/common/Toast';
import { useTranslations } from 'next-intl';
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/animate-ui/components/animate/tooltip';

/** Channel card height: h-54 = 216px */
const CHANNEL_CARD_HEIGHT = 216;

export function Channel() {
    const { data: channelsData } = useChannelList();
    const syncChannel = useSyncChannel();
    const t = useTranslations('channel.batchSync');
    const pageKey = 'channel' as const;
    const pageSize = useGridPageSize({
        itemHeight: CHANNEL_CARD_HEIGHT,
        gap: 16,
        columns: { default: 1, md: 2, lg: 3, xl: 4 },
    });
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const page = usePaginationStore((s) => s.getPage(pageKey));
    const setPage = usePaginationStore((s) => s.setPage);
    const setTotalItems = usePaginationStore((s) => s.setTotalItems);
    const setPageSize = usePaginationStore((s) => s.setPageSize);
    const direction = usePaginationStore((s) => s.getDirection(pageKey));

    const filteredChannels = useMemo(() => {
        if (!channelsData) return [];
        const sorted = [...channelsData].sort((a, b) => a.raw.id - b.raw.id);
        if (!searchTerm.trim()) return sorted;
        const term = searchTerm.toLowerCase();
        return sorted.filter((c) => c.raw.name.toLowerCase().includes(term));
    }, [channelsData, searchTerm]);

    // Sync to store for Toolbar to display pagination info
    useEffect(() => {
        setTotalItems(pageKey, filteredChannels.length);
        setPageSize(pageKey, pageSize);
    }, [filteredChannels.length, pageSize, pageKey, setTotalItems, setPageSize]);

    // Reset to page 1 when search term changes
    useEffect(() => {
        setPage(pageKey, 1);
    }, [searchTerm, pageKey, setPage]);

    const pagedChannels = useMemo(() => {
        const start = (page - 1) * pageSize;
        return filteredChannels.slice(start, start + pageSize);
    }, [filteredChannels, page, pageSize]);

    // 批量同步处理
    const handleBatchSync = () => {
        // 检查是否有启用自动同步的渠道
        const autoSyncChannels = channelsData?.filter(c => c.raw.auto_sync) || [];
        
        if (autoSyncChannels.length === 0) {
            toast.error(t('noAutoSyncChannels'));
            return;
        }

        syncChannel.mutate(undefined, {
            onSuccess: () => {
                toast.success(t('success'));
            },
            onError: (error) => {
                toast.error(error.message || t('failed'));
            },
        });
    };

    return (
        <>
            {/* 批量同步按钮 - 显示在内容区域顶部 */}
            <div className="mb-4 flex justify-end">
                <Tooltip side="bottom" sideOffset={10} align="end">
                    <TooltipTrigger asChild>
                        <Button
                            onClick={handleBatchSync}
                            disabled={syncChannel.isPending}
                            variant="outline"
                            size="sm"
                            className="rounded-xl gap-2"
                        >
                            <RefreshCw className={`size-4 ${syncChannel.isPending ? 'animate-spin' : ''}`} />
                            <span className="hidden sm:inline">
                                {syncChannel.isPending ? t('syncing') : t('button')}
                            </span>
                            <span className="sm:hidden">
                                {syncChannel.isPending ? t('syncing') : t('buttonMobile')}
                            </span>
                        </Button>
                    </TooltipTrigger>
                    <TooltipContent>
                        {t('button')}
                    </TooltipContent>
                </Tooltip>
            </div>

            <AnimatePresence mode="popLayout" initial={false} custom={direction}>
                <motion.div
                    key={`channel-page-${page}`}
                    custom={direction}
                    variants={{
                        enter: (d: number) => ({ x: d >= 0 ? 24 : -24, opacity: 0 }),
                        center: { x: 0, opacity: 1 },
                        exit: (d: number) => ({ x: d >= 0 ? -24 : 24, opacity: 0 }),
                    }}
                    initial="enter"
                    animate="center"
                    exit="exit"
                    transition={{ duration: 0.25, ease: EASING.easeOutExpo }}
                >
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
                        <AnimatePresence mode="popLayout">
                            {pagedChannels.map((channel, index) => (
                                <motion.div
                                    key={"channel-" + channel.raw.id}
                                    initial={{ opacity: 0, y: 20 }}
                                    animate={{ opacity: 1, y: 0 }}
                                    exit={{
                                        opacity: 0,
                                        scale: 0.95,
                                        transition: { duration: 0.2 }
                                    }}
                                    transition={{
                                        duration: 0.45,
                                        ease: EASING.easeOutExpo,
                                        delay: index === 0 ? 0 : Math.min(0.08 * Math.log2(index + 1), 0.4),
                                    }}
                                    layout={!searchTerm.trim()}
                                >
                                    <Card channel={channel.raw} stats={channel.formatted} />
                                </motion.div>
                            ))}
                        </AnimatePresence>
                    </div>
                </motion.div>
            </AnimatePresence>
        </>
    );
}
