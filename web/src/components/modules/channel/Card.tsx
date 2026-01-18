import { useState, useCallback, useMemo } from 'react';
import {
    MorphingDialog,
    MorphingDialogTrigger,
    MorphingDialogContainer,
    MorphingDialogContent,
} from '@/components/ui/morphing-dialog';
import { DollarSign, MessageSquare, FlaskConical } from 'lucide-react';
import { type StatsMetricsFormatted } from '@/api/endpoints/stats';
import { type Channel, useEnableChannel } from '@/api/endpoints/channel';
import { CardContent } from './CardContent';
import { useTranslations } from 'next-intl';
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/animate-ui/components/animate/tooltip';
import { Switch } from '@/components/ui/switch';
import { toast } from '@/components/common/Toast';
import { Button } from '@/components/ui/button';
import { BatchTestDialog, type TestTarget } from '@/components/common/BatchTestDialog';
import { ModelSelectionDialog, type ModelOption } from '@/components/common/ModelSelectionDialog';

export function Card({ channel, stats }: { channel: Channel; stats: StatsMetricsFormatted }) {
    const t = useTranslations('channel.card');
    const tTest = useTranslations('test');
    const enableChannel = useEnableChannel();
    
    // 批量测试相关状态
    const [isModelSelectionOpen, setIsModelSelectionOpen] = useState(false);
    const [isTestDialogOpen, setIsTestDialogOpen] = useState(false);
    const [selectedModelsForTest, setSelectedModelsForTest] = useState<string[]>([]);

    const handleEnableChange = (checked: boolean) => {
        enableChannel.mutate(
            { id: channel.id, enabled: checked },
            {
                onSuccess: () => {
                    toast.success(checked ? t('toast.enabled') : t('toast.disabled'));
                },
                onError: (error) => {
                    toast.error(error.message);
                },
            }
        );
    };

    // 获取所有模型
    const getAllModels = useCallback((): string[] => {
        const models: string[] = [];
        if (channel.model) {
            models.push(...channel.model.split(',').map(m => m.trim()).filter(Boolean));
        }
        if (channel.custom_model) {
            models.push(...channel.custom_model.split(',').map(m => m.trim()).filter(Boolean));
        }
        return models;
    }, [channel.model, channel.custom_model]);

    // 获取模型选项
    const getModelOptions = useMemo((): ModelOption[] => {
        return getAllModels().map(model => ({
            model,
            channelId: channel.id,
            channelName: channel.name,
        }));
    }, [getAllModels, channel.id, channel.name]);

    // 获取测试目标
    const getTestTargets = useCallback((models?: string[]): TestTarget[] => {
        const targetModels = models || getAllModels();
        return targetModels.map(model => ({
            channelId: channel.id,
            channelName: channel.name,
            model,
        }));
    }, [getAllModels, channel.id, channel.name]);

    // 打开模型选择对话框
    const handleOpenModelSelection = useCallback((e: React.MouseEvent) => {
        e.stopPropagation();
        setIsModelSelectionOpen(true);
    }, []);

    // 确认模型选择
    const handleConfirmModelSelection = useCallback((models: string[]) => {
        setSelectedModelsForTest(models);
        // 延迟打开批量测试对话框，确保模型选择对话框完全关闭
        setTimeout(() => {
            setIsTestDialogOpen(true);
        }, 100);
    }, []);

    // 测试对话框关闭
    const handleTestDialogClose = useCallback((open: boolean) => {
        setIsTestDialogOpen(open);
        if (!open) {
            // 测试对话框关闭时重置选择
            setSelectedModelsForTest([]);
        }
    }, []);

    return (
        <>
            <MorphingDialog>
                <MorphingDialogTrigger className="w-full">
                    <article className="relative flex h-54 flex-col justify-between gap-5 rounded-3xl border border-border bg-card text-card-foreground p-4 custom-shadow transition-all duration-300 hover:scale-[1.02]">
                        <header className="relative flex items-center justify-between gap-2">
                            <Tooltip side="top" sideOffset={10} align="center">
                                <TooltipTrigger asChild>
                                    <h3 className="text-lg font-bold truncate min-w-0">{channel.name}</h3>
                                </TooltipTrigger>
                                <TooltipContent key={channel.name}>{channel.name}</TooltipContent>
                            </Tooltip>
                            <div className="flex items-center gap-2">
                                {/* 批量测试按钮 */}
                                <Tooltip side="top" sideOffset={10} align="center">
                                    <TooltipTrigger asChild>
                                        <Button
                                            size="icon"
                                            variant="ghost"
                                            className="h-8 w-8 rounded-lg"
                                            onClick={handleOpenModelSelection}
                                            disabled={getAllModels().length === 0}
                                        >
                                            <FlaskConical className="h-4 w-4" />
                                        </Button>
                                    </TooltipTrigger>
                                    <TooltipContent>{tTest('batchTest')}</TooltipContent>
                                </Tooltip>
                                <Switch
                                    checked={channel.enabled}
                                    onCheckedChange={handleEnableChange}
                                    disabled={enableChannel.isPending}
                                    onClick={(e) => e.stopPropagation()}
                                />
                            </div>
                        </header>

                        <dl className="relative grid grid-cols-1 gap-3">
                            <div className="flex items-center justify-between rounded-2xl border border-border/70 bg-background/80 p-2">
                                <div className="flex items-center gap-3">
                                    <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary">
                                        <MessageSquare className="h-5 w-5" />
                                    </span>
                                    <dt className="text-sm text-muted-foreground">{t('requestCount')}</dt>
                                </div>
                                <dd className="text-base">
                                    {stats.request_count.formatted.value}
                                    <span className="ml-1 text-xs text-muted-foreground">{stats.request_count.formatted.unit}</span>
                                </dd>
                            </div>

                            <div className="flex items-center justify-between rounded-2xl border border-border/70 bg-background/80 p-2">
                                <div className="flex items-center gap-3">
                                    <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary">
                                        <DollarSign className="h-5 w-5" />
                                    </span>
                                    <dt className="text-sm text-muted-foreground">{t('totalCost')}</dt>
                                </div>
                                <dd className="text-base">
                                    {stats.total_cost.formatted.value}
                                    <span className="ml-1 text-xs text-muted-foreground">{stats.total_cost.formatted.unit}</span>
                                </dd>
                            </div>
                        </dl>
                    </article>
                </MorphingDialogTrigger>

                <MorphingDialogContainer>
                    <MorphingDialogContent className="w-full md:max-w-xl bg-card text-card-foreground px-4 py-2 custom-shadow rounded-3xl max-h-[90vh] overflow-y-auto">
                        <CardContent channel={channel} stats={stats} />
                    </MorphingDialogContent>
                </MorphingDialogContainer>
            </MorphingDialog>

            {/* 模型选择对话框 */}
            <ModelSelectionDialog
                open={isModelSelectionOpen}
                onOpenChange={setIsModelSelectionOpen}
                models={getModelOptions}
                initialSelected={selectedModelsForTest}
                onConfirm={handleConfirmModelSelection}
                title={`${tTest('selectModels')}: ${channel.name}`}
            />

            {/* 批量测试对话框 */}
            <BatchTestDialog
                open={isTestDialogOpen}
                onOpenChange={handleTestDialogClose}
                targets={isTestDialogOpen ? getTestTargets(selectedModelsForTest.length > 0 ? selectedModelsForTest : undefined) : []}
            />
        </>
    );
}
