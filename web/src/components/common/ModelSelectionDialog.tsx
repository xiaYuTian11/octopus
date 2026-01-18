'use client';

import { useState, useMemo, useCallback, useEffect, useRef } from 'react';
import { Search, CheckSquare, Square } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogFooter,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

export interface ModelOption {
    model: string;
    channelId: number;
    channelName: string;
}

interface ModelSelectionDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    models: ModelOption[];
    initialSelected?: string[];
    onConfirm: (selectedModels: string[]) => void;
    title?: string;
}

// 防抖 Hook
function useDebounce<T>(value: T, delay: number): T {
    const [debouncedValue, setDebouncedValue] = useState<T>(value);

    useEffect(() => {
        const handler = setTimeout(() => {
            setDebouncedValue(value);
        }, delay);

        return () => {
            clearTimeout(handler);
        };
    }, [value, delay]);

    return debouncedValue;
}

export function ModelSelectionDialog({
    open,
    onOpenChange,
    models,
    initialSelected,
    onConfirm,
    title,
}: ModelSelectionDialogProps) {
    const t = useTranslations('test');
    const [searchQuery, setSearchQuery] = useState('');
    const debouncedSearchQuery = useDebounce(searchQuery, 150);
    const [selectedModels, setSelectedModels] = useState<Set<string>>(() =>
        new Set(initialSelected && initialSelected.length > 0 ? initialSelected : models.map(m => m.model))
    );
    const searchInputRef = useRef<HTMLInputElement>(null);

    // 当对话框打开时，重置选择状态并聚焦搜索框
    useEffect(() => {
        if (open) {
            setSearchQuery('');
            setSelectedModels(new Set(initialSelected && initialSelected.length > 0 ? initialSelected : models.map(m => m.model)));
            // 延迟聚焦，确保对话框动画完成
            setTimeout(() => {
                searchInputRef.current?.focus();
            }, 100);
        }
    }, [open, models, initialSelected]);

    const filteredModels = useMemo(() => {
        if (!debouncedSearchQuery.trim()) return models;
        const query = debouncedSearchQuery.toLowerCase().trim();
        return models.filter(m => m.model.toLowerCase().includes(query));
    }, [models, debouncedSearchQuery]);

    // 计算当前过滤结果中有多少已被选中
    const selectedFilteredCount = useMemo(() => {
        return filteredModels.filter(m => selectedModels.has(m.model)).length;
    }, [filteredModels, selectedModels]);

    const handleToggle = useCallback((model: string) => {
        setSelectedModels(prev => {
            const next = new Set(prev);
            if (next.has(model)) {
                next.delete(model);
            } else {
                next.add(model);
            }
            return next;
        });
    }, []);

    const handleToggleAll = useCallback(() => {
        // 判断当前过滤结果是否全部选中
        const allFilteredSelected = filteredModels.length > 0 &&
            selectedFilteredCount === filteredModels.length;
        
        setSelectedModels(prev => {
            const next = new Set(prev);
            if (allFilteredSelected) {
                // 取消选中当前过滤结果中的所有项
                filteredModels.forEach(m => next.delete(m.model));
            } else {
                // 选中当前过滤结果中的所有项
                filteredModels.forEach(m => next.add(m.model));
            }
            return next;
        });
    }, [filteredModels, selectedFilteredCount]);

    const handleConfirm = useCallback(() => {
        onConfirm(Array.from(selectedModels));
        onOpenChange(false);
    }, [selectedModels, onConfirm, onOpenChange]);

    // 判断当前过滤结果是否全部选中
    const allFilteredSelected = filteredModels.length > 0 && selectedFilteredCount === filteredModels.length;

    return (
        <Dialog open={open} onOpenChange={onOpenChange} modal={false}>
            <DialogContent className="sm:max-w-lg max-h-[90vh] flex flex-col">
                <DialogHeader>
                    <DialogTitle>{title || t('selectModels')}</DialogTitle>
                </DialogHeader>

                <div className="space-y-4 flex-1 min-h-0 flex flex-col">
                    <div className="relative">
                        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-4 text-muted-foreground pointer-events-none" />
                        <Input
                            ref={searchInputRef}
                            type="text"
                            value={searchQuery}
                            onChange={(e) => setSearchQuery(e.target.value)}
                            placeholder={t('searchModels')}
                            className="pl-8"
                        />
                    </div>

                    <div className="flex items-center justify-between text-sm">
                        <span className="text-muted-foreground">
                            {t('selectedCount', { count: selectedModels.size, total: models.length })}
                        </span>
                        <button
                            type="button"
                            onClick={handleToggleAll}
                            className="flex items-center gap-1.5 text-primary hover:underline min-h-[44px] sm:min-h-0 px-2 -mx-2"
                        >
                            {allFilteredSelected ? (
                                <>
                                    <CheckSquare className="size-4" />
                                    {t('deselectAll')}
                                </>
                            ) : (
                                <>
                                    <Square className="size-4" />
                                    {t('selectAll')}
                                </>
                            )}
                        </button>
                    </div>

                    <div className="flex-1 min-h-0 overflow-y-auto space-y-2 border rounded-xl p-2">
                        {filteredModels.map((m) => {
                            const isSelected = selectedModels.has(m.model);
                            return (
                                <button
                                    key={m.model}
                                    type="button"
                                    onClick={() => handleToggle(m.model)}
                                    className={cn(
                                        'w-full flex items-center gap-3 p-3 rounded-lg border transition-all text-left',
                                        'min-h-[56px] sm:min-h-0 active:scale-[0.98]',
                                        isSelected
                                            ? 'border-primary bg-primary/5'
                                            : 'border-border hover:bg-muted'
                                    )}
                                >
                                    {isSelected ? (
                                        <CheckSquare className="size-5 text-primary shrink-0" />
                                    ) : (
                                        <Square className="size-5 text-muted-foreground shrink-0" />
                                    )}
                                    <div className="min-w-0 flex-1">
                                        <div className="text-sm font-medium truncate">{m.model}</div>
                                    </div>
                                </button>
                            );
                        })}
                        {filteredModels.length === 0 && (
                            <div className="text-center py-8 text-sm text-muted-foreground">
                                {t('noModelsFound')}
                            </div>
                        )}
                    </div>
                </div>

                <DialogFooter className="gap-2 sm:gap-0">
                    <Button
                        variant="outline"
                        onClick={() => onOpenChange(false)}
                        className="min-h-[44px] sm:min-h-10"
                    >
                        {t('cancel')}
                    </Button>
                    <Button
                        onClick={handleConfirm}
                        disabled={selectedModels.size === 0}
                        className="min-h-[44px] sm:min-h-10"
                    >
                        {t('confirmTest', { count: selectedModels.size })}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
