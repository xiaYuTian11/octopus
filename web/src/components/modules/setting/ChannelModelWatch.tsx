'use client';

import { useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Bell, Link2, Clock, Trash2 } from 'lucide-react';
import {
    useChannelList,
    useChannelModelWatchList,
    useCreateChannelModelWatch,
    useDeleteChannelModelWatch,
    useUpdateChannelModelWatch,
    useTestChannelModelWatch,
} from '@/api/endpoints/channel';
import { Input } from '@/components/ui/input';
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { toast } from '@/components/common/Toast';

type WatchForm = {
    channel_id: string;
    model_name: string;
    webhook_url: string;
    secret: string;
    dedup_minutes: string;
    enabled: boolean;
};

const defaultForm: WatchForm = {
    channel_id: '',
    model_name: '',
    webhook_url: '',
    secret: '',
    dedup_minutes: '60',
    enabled: true,
};

export function SettingChannelModelWatch() {
    const t = useTranslations('setting');
    const { data: channels } = useChannelList();
    const { data: watches, isLoading } = useChannelModelWatchList();
    const createWatch = useCreateChannelModelWatch();
    const updateWatch = useUpdateChannelModelWatch();
    const deleteWatch = useDeleteChannelModelWatch();
    const testWatch = useTestChannelModelWatch();

    const [form, setForm] = useState<WatchForm>(defaultForm);

    const channelMap = useMemo(() => {
        const map = new Map<number, string>();
        channels?.forEach((c) => map.set(c.raw.id, c.raw.name));
        return map;
    }, [channels]);

    const handleCreate = () => {
        if (!form.channel_id || !form.model_name.trim() || !form.webhook_url.trim()) {
            toast.error(t('channelModelWatch.form.missingRequired'));
            return;
        }
        const dedup = parseInt(form.dedup_minutes, 10);
        createWatch.mutate(
            {
                channel_id: Number(form.channel_id),
                model_name: form.model_name.trim(),
                webhook_url: form.webhook_url.trim(),
                secret: form.secret.trim() || undefined,
                dedup_minutes: Number.isFinite(dedup) ? dedup : undefined,
                enabled: form.enabled,
            },
            {
                onSuccess: () => {
                    setForm(defaultForm);
                    toast.success(t('channelModelWatch.form.created'));
                },
                onError: (err) => {
                    const msg = err instanceof Error ? err.message : String(err);
                    toast.error(t('channelModelWatch.form.createFailed'), { description: msg });
                },
            }
        );
    };

    const formatTime = (str?: string | null) => {
        if (!str) return t('channelModelWatch.never');
        const d = new Date(str);
        if (Number.isNaN(d.getTime())) return t('channelModelWatch.never');
        return d.toLocaleString();
    };

    return (
        <div className="rounded-3xl border border-border bg-card p-6 custom-shadow space-y-5">
            <div className="flex items-center gap-2 text-lg font-bold text-card-foreground">
                <Bell className="h-5 w-5" />
                {t('channelModelWatch.title')}
            </div>

            <div className="space-y-3">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                    <div className="space-y-2">
                        <div className="text-sm font-medium flex items-center gap-2">
                            <Link2 className="h-4 w-4 text-muted-foreground" />
                            {t('channelModelWatch.form.channel')}
                        </div>
                        <Select
                            value={form.channel_id}
                            onValueChange={(v) => setForm((prev) => ({ ...prev, channel_id: v }))}
                        >
                            <SelectTrigger className="w-full rounded-xl">
                                <SelectValue placeholder={t('channelModelWatch.form.channelPlaceholder')} />
                            </SelectTrigger>
                            <SelectContent>
                                {channels?.map((c) => (
                                    <SelectItem key={c.raw.id} value={String(c.raw.id)}>
                                        {c.raw.name}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                    </div>

                    <div className="space-y-2">
                        <div className="text-sm font-medium">{t('channelModelWatch.form.model')}</div>
                        <Input
                            value={form.model_name}
                            onChange={(e) => setForm((prev) => ({ ...prev, model_name: e.target.value }))}
                            placeholder="gpt-4.1, claude-3.5-sonnet..."
                            className="rounded-xl"
                        />
                    </div>

                    <div className="space-y-2">
                        <div className="text-sm font-medium">{t('channelModelWatch.form.webhook')}</div>
                        <Input
                            value={form.webhook_url}
                            onChange={(e) => setForm((prev) => ({ ...prev, webhook_url: e.target.value }))}
                            placeholder="https://example.com/webhook"
                            className="rounded-xl"
                        />
                    </div>

                    <div className="space-y-2">
                        <div className="text-sm font-medium">{t('channelModelWatch.form.secret')}</div>
                        <Input
                            value={form.secret}
                            onChange={(e) => setForm((prev) => ({ ...prev, secret: e.target.value }))}
                            placeholder={t('channelModelWatch.form.secretPlaceholder')}
                            className="rounded-xl"
                        />
                    </div>

                    <div className="space-y-2">
                        <div className="text-sm font-medium flex items-center gap-2">
                            <Clock className="h-4 w-4 text-muted-foreground" />
                            {t('channelModelWatch.form.dedup')}
                        </div>
                        <Input
                            type="number"
                            value={form.dedup_minutes}
                            onChange={(e) => setForm((prev) => ({ ...prev, dedup_minutes: e.target.value }))}
                            placeholder="60"
                            className="rounded-xl"
                        />
                    </div>

                    <div className="flex items-center justify-between rounded-2xl border border-dashed px-4 py-3">
                        <div className="flex flex-col">
                            <span className="text-sm font-medium">{t('channelModelWatch.form.enabled')}</span>
                            <span className="text-xs text-muted-foreground">{t('channelModelWatch.form.enabledDesc')}</span>
                        </div>
                        <Switch
                            checked={form.enabled}
                            onCheckedChange={(checked) => setForm((prev) => ({ ...prev, enabled: checked }))}
                        />
                    </div>
                </div>

                <div className="flex justify-end">
                    <Button
                        variant="default"
                        className="rounded-xl"
                        onClick={handleCreate}
                        disabled={createWatch.isPending}
                    >
                        {createWatch.isPending ? t('channelModelWatch.form.creating') : t('channelModelWatch.form.create')}
                    </Button>
                </div>
            </div>

            <div className="space-y-3">
                <div className="flex items-center justify-between">
                    <div className="text-sm font-semibold text-muted-foreground">
                        {t('channelModelWatch.listTitle')}
                    </div>
                    {isLoading && <span className="text-xs text-muted-foreground">{t('loading')}</span>}
                </div>

                <div className="space-y-3">
                    {watches?.length === 0 && (
                        <div className="text-sm text-muted-foreground">{t('channelModelWatch.empty')}</div>
                    )}
                    {watches?.map((item) => (
                        <div
                            key={item.id}
                            className="flex flex-col md:flex-row md:items-center md:justify-between gap-3 rounded-2xl border border-border/60 p-3"
                        >
                            <div className="space-y-1">
                                <div className="text-sm font-semibold">
                                    {channelMap.get(item.channel_id) || `#${item.channel_id}`} - {item.model_name}
                                </div>
                                <div className="text-xs text-muted-foreground break-all">{item.webhook_url}</div>
                                <div className="text-xs text-muted-foreground">
                                    {t('channelModelWatch.dedupTip', { minutes: item.dedup_minutes || 60 })}
                                </div>
                                <div className="text-xs text-muted-foreground">
                                    {t('channelModelWatch.lastNotified', { time: formatTime(item.last_notified_at) })}
                                </div>
                            </div>
                            <div className="flex items-center gap-3">
                                    <Switch
                                        checked={item.enabled}
                                        onCheckedChange={(checked) =>
                                            updateWatch.mutate({ id: item.id, enabled: checked })
                                        }
                                    />
                                    <Button
                                        variant="outline"
                                        size="sm"
                                        onClick={() =>
                                            testWatch.mutate(
                                                { id: item.id },
                                                {
                                                    onSuccess: () => toast.success(t('channelModelWatch.test.success')),
                                                    onError: (err) => toast.error(t('channelModelWatch.test.failed'), { description: err.message }),
                                                }
                                            )
                                        }
                                        disabled={testWatch.isPending}
                                        className="rounded-xl"
                                    >
                                        {testWatch.isPending ? t('channelModelWatch.test.testing') : t('channelModelWatch.test.action')}
                                    </Button>
                                    <Button
                                        variant="ghost"
                                        size="icon"
                                        className="text-destructive hover:text-destructive"
                                        onClick={() =>
                                        deleteWatch.mutate(item.id, {
                                            onSuccess: () => toast.success(t('channelModelWatch.deleted')),
                                        })
                                    }
                                >
                                    <Trash2 className="h-4 w-4" />
                                </Button>
                            </div>
                        </div>
                    ))}
                </div>
            </div>
        </div>
    );
}
