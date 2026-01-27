import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../client';
import { logger } from '@/lib/logger';
import { formatCount, formatMoney, formatTime } from '@/lib/utils';
import { StatsChannel, type StatsMetricsFormatted } from './stats';
/**
 * 渠道类型枚举
 */
export enum ChannelType {
    OpenAIChat = 0,
    OpenAIResponse = 1,
    Anthropic = 2,
    Gemini = 3,
    Volcengine = 4,
    OpenAIEmbedding = 5,
}

/**
 * 自动分组类型枚举
 */
export enum AutoGroupType {
    None = 0,   // 不自动分组
    Fuzzy = 1,  // 模糊匹配
    Exact = 2,  // 准确匹配
    Regex = 3,  // 正则匹配
}

export type BaseUrl = {
    url: string;
    delay: number;
};

export type CustomHeader = {
    header_key: string;
    header_value: string;
};

export type ChannelKey = {
    id: number;
    channel_id: number;
    enabled: boolean;
    channel_key: string;
    status_code: number;
    last_use_time_stamp: number;
    total_cost: number;
    failure_count?: number;
    disabled_reason?: string;
    remark: string;
    rate_limit_rpm?: number; // 每分钟最大请求数，0 表示不限制
};

/**
 * 渠道完整数据（与后端 model.Channel 对齐；数组字段在前端保证为 []）
 */
export type Channel = {
    id: number;
    name: string;
    type: ChannelType;
    enabled: boolean;
    base_urls: BaseUrl[];
    keys: ChannelKey[];
    model: string;
    custom_model: string;
    proxy: boolean;
    auto_sync: boolean;
    auto_group: AutoGroupType;
    custom_header: CustomHeader[];
    key_pool_enabled?: boolean;
    key_fail_threshold?: number;
    key_count?: number;
    key_enabled_count?: number;
    key_disabled_count?: number;
    param_override?: string | null;
    channel_proxy?: string | null;
    match_regex?: string | null;
    stats: StatsChannel;
};

// Internal type: backend may return null for slice fields; normalize to [] in select()
type ChannelServer = Omit<Channel, 'base_urls' | 'custom_header' | 'keys'> & {
    base_urls: BaseUrl[] | null;
    custom_header: CustomHeader[] | null;
    keys: ChannelKey[] | null;
};

/**
 * 创建渠道请求：必填字段 + 可选字段
 */
export type CreateChannelRequest = {
    name: string;
    type: ChannelType;
    enabled?: boolean;
    base_urls: BaseUrl[];
    keys: Array<Pick<ChannelKey, 'enabled' | 'channel_key' | 'remark' | 'rate_limit_rpm'>>;
    model: string;
    custom_model?: string;
    proxy?: boolean;
    auto_sync?: boolean;
    auto_group?: AutoGroupType;
    key_pool_enabled?: boolean;
    key_fail_threshold?: number;
    custom_header?: CustomHeader[];
    channel_proxy?: string | null;
    param_override?: string | null;
    match_regex?: string | null;
};

/**
 * 更新渠道请求：id + 可选字段 + keys diff
 */
export type UpdateChannelRequest = {
    id: number;
    name?: string;
    type?: ChannelType;
    enabled?: boolean;
    base_urls?: BaseUrl[];
    model?: string;
    custom_model?: string;
    proxy?: boolean;
    auto_sync?: boolean;
    auto_group?: AutoGroupType;
    key_pool_enabled?: boolean;
    key_fail_threshold?: number;
    custom_header?: CustomHeader[];
    channel_proxy?: string | null;
    param_override?: string | null;
    match_regex?: string | null;
    // keys diff
    keys_to_add?: Array<Pick<ChannelKey, 'enabled' | 'channel_key' | 'remark' | 'rate_limit_rpm'>>;
    keys_to_update?: Array<{ id: number; enabled?: boolean; channel_key?: string; remark?: string; rate_limit_rpm?: number }>;
    keys_to_delete?: number[];
};

export type FetchModelRequest = {
    id?: number;
    type: ChannelType;
    base_urls: BaseUrl[];
    keys: Array<Pick<ChannelKey, 'enabled' | 'channel_key'>>;
    proxy?: boolean;
    match_regex?: string | null;
    enabled?: boolean;
};

/**
 * 获取渠道列表 Hook
 * 
 * @example
 * const { data: channels, isLoading, error } = useChannelList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * channels?.forEach(channel => console.log(channel.raw.name));
 */
export function useChannelList() {
    return useQuery({
        queryKey: ['channels', 'list'],
        queryFn: async () => {
            return apiClient.get<ChannelServer[]>('/api/v1/channel/list');
        },
        select: (data) => data.map((item) => ({
            raw: ({
                ...item,
                base_urls: item.base_urls ?? [],
                custom_header: item.custom_header ?? [],
                keys: item.keys ?? [],
                key_pool_enabled: !!item.key_pool_enabled,
                key_fail_threshold: item.key_fail_threshold ?? 3,
                key_count: item.key_count ?? item.keys?.length ?? 0,
                key_enabled_count: item.key_enabled_count ?? item.keys?.filter((k) => k.enabled)?.length ?? 0,
                key_disabled_count:
                    item.key_disabled_count ??
                    (item.keys ? item.keys.length - item.keys.filter((k) => k.enabled).length : 0),
            }) satisfies Channel,
            formatted: {
                input_token: formatCount(item.stats.input_token),
                output_token: formatCount(item.stats.output_token),
                total_token: formatCount(item.stats.input_token + item.stats.output_token),
                input_cost: formatMoney(item.stats.input_cost),
                output_cost: formatMoney(item.stats.output_cost),
                total_cost: formatMoney(item.stats.input_cost + item.stats.output_cost),
                request_success: formatCount(item.stats.request_success),
                request_failed: formatCount(item.stats.request_failed),
                request_count: formatCount(item.stats.request_success + item.stats.request_failed),
                wait_time: formatTime(item.stats.wait_time),
            }
        })) as Array<{ raw: Channel; formatted: StatsMetricsFormatted }>,
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}

/**
 * 创建渠道 Hook
 * 
 * @example
 * const createChannel = useCreateChannel();
 * 
 * createChannel.mutate({
 *   name: 'OpenAI',
 *   type: ChannelType.OpenAIChat,
 *   base_urls: [{ url: 'https://api.openai.com', delay: 0 }],
 *   keys: [{ enabled: true, channel_key: 'sk-xxx' }],
 *   model: 'gpt-4',
 * });
 */
export function useCreateChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: CreateChannelRequest) => {
            return apiClient.post<ChannelServer>('/api/v1/channel/create', data);
        },
        onSuccess: (data) => {
            logger.log('渠道创建成功:', data);
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道创建失败:', error);
        },
    });
}

/**
 * 更新渠道 Hook
 * 
 * @example
 * const updateChannel = useUpdateChannel();
 * 
 * updateChannel.mutate({
 *   id: 1,
 *   name: 'OpenAI Updated',
 *   type: ChannelType.OpenAIChat,
 *   enabled: true,
 *   base_urls: [{ url: 'https://api.openai.com', delay: 0 }],
 *   keys_to_add: [{ enabled: true, channel_key: 'sk-xxx' }],
 *   model: 'gpt-4-turbo',
 *   proxy: false,
 * });
 */
export function useUpdateChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: UpdateChannelRequest) => {
            return apiClient.post<ChannelServer>('/api/v1/channel/update', data);
        },
        onSuccess: (data) => {
            logger.log('渠道更新成功:', data);
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道更新失败:', error);
        },
    });
}

/**
 * 删除渠道 Hook
 * 
 * @example
 * const deleteChannel = useDeleteChannel();
 * 
 * deleteChannel.mutate(1); // 删除 ID 为 1 的渠道
 */
export function useDeleteChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (id: number) => {
            return apiClient.delete<null>(`/api/v1/channel/delete/${id}`);
        },
        onSuccess: () => {
            logger.log('渠道删除成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道删除失败:', error);
        },
    });
}

/**
 * 启用/禁用渠道 Hook
 * 
 * @example
 * const enableChannel = useEnableChannel();
 * 
 * enableChannel.mutate({ id: 1, enabled: true }); // 启用 ID 为 1 的渠道
 * enableChannel.mutate({ id: 1, enabled: false }); // 禁用 ID 为 1 的渠道
 */
export function useEnableChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: { id: number; enabled: boolean }) => {
            return apiClient.post<null>('/api/v1/channel/enable', data);
        },
        onSuccess: () => {
            logger.log('渠道状态更新成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道状态更新失败:', error);
        },
    });
}

/**
 * 获取渠道模型列表 Hook
 * 
 * @example
 * const fetchModel = useFetchModel();
 * 
 * fetchModel.mutate({
 *   type: ChannelType.OpenAIChat,
 *   base_urls: [{ url: 'https://api.openai.com', delay: 0 }],
 *   keys: [{ enabled: true, channel_key: 'sk-xxx' }],
 *   proxy: false,
 * });
 * 
 * // 在 onSuccess 中获取模型列表
 * fetchModel.data // ['gpt-4', 'gpt-3.5-turbo', ...]
 */
export function useFetchModel() {
    return useMutation({
        mutationFn: async (data: FetchModelRequest) => {
            return apiClient.post<string[]>('/api/v1/channel/fetch-model', data);
        },
        onSuccess: (data) => {
            logger.log('模型列表获取成功:', data);
        },
        onError: (error) => {
            logger.error('模型列表获取失败:', error);
        },
    });
}

export type ChannelModelWatch = {
    id: number;
    channel_id: number;
    model_name: string;
    webhook_url: string;
    secret: string;
    dedup_minutes: number;
    enabled: boolean;
    last_notified_at?: string | null;
    created_at: string;
    updated_at: string;
};

export type ChannelModelWatchCreateRequest = {
    channel_id: number;
    model_name: string;
    webhook_url: string;
    secret?: string;
    dedup_minutes?: number;
    enabled?: boolean;
};

export type ChannelModelWatchUpdateRequest = {
    id: number;
    channel_id?: number;
    model_name?: string;
    webhook_url?: string;
    secret?: string;
    dedup_minutes?: number;
    enabled?: boolean;
};

export function useChannelModelWatchList(channelId?: number) {
    return useQuery({
        queryKey: ['channel', 'model-watch', channelId ?? 'all'],
        queryFn: async () => {
            const params = channelId ? { channel_id: channelId } : undefined;
            return apiClient.get<ChannelModelWatch[]>('/api/v1/channel/watch/list', params as any);
        },
        refetchInterval: 30000,
    });
}

export function useCreateChannelModelWatch() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (data: ChannelModelWatchCreateRequest) => {
            return apiClient.post<ChannelModelWatch>('/api/v1/channel/watch/create', data);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channel', 'model-watch'] });
        },
    });
}

export function useUpdateChannelModelWatch() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (data: ChannelModelWatchUpdateRequest) => {
            return apiClient.post<ChannelModelWatch>('/api/v1/channel/watch/update', data);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channel', 'model-watch'] });
        },
    });
}

export function useDeleteChannelModelWatch() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (id: number) => {
            return apiClient.delete<null>(`/api/v1/channel/watch/delete/${id}`);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channel', 'model-watch'] });
        },
    });
}

/**
 * 获取渠道最后同步时间 Hook
 * 
 * @example
 * const lastSyncTime = useLastSyncTime();
 * 
 * if (lastSyncTime) {
 *   console.log('最后同步时间:', new Date(lastSyncTime).toLocaleString());
 * }
 */
export function useLastSyncTime() {
    return useQuery({
        queryKey: ['channels', 'last-sync-time'],
        queryFn: async () => {
            return apiClient.get<string>('/api/v1/channel/last-sync-time');
        },
        refetchInterval: 30000,
    });
}
/**
 * 同步渠道 Hook
 * 
 * @example
 * const syncChannel = useSyncChannel();
 * 
 * syncChannel.mutate();
 */
export function useSyncChannel() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async () => {
            return apiClient.post<null>('/api/v1/channel/sync');
        },
        onSuccess: () => {
            logger.log('渠道同步成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'last-sync-time'] });
        },
        onError: (error) => {
            logger.error('渠道同步失败:', error);
        },
    });
}

/**
 * 同步单个渠道
 * 
 * @param channelId 渠道 ID
 * @returns Promise<null>
 */
export async function syncSingleChannel(channelId: number): Promise<null> {
    return apiClient.post<null>(`/api/v1/channel/sync/${channelId}`);
}

/**
 * 测试渠道请求
 */
export type TestChannelRequest = {
    channel_id: number;
    model: string;
};

/**
 * 测试渠道响应
 */
export type TestChannelResponse = {
    success: boolean;
    latency: number;
    error?: string;
};

export type ValidateChannelKeysRequest = {
    channel_id: number;
    model: string;
    timeout?: number;
    concurrency?: number;
    mode?: string;
};

export type ValidateChannelKeysResponse = {
    tested: number;
    disabled: number;
    success: number;
};

// ---------- 新增：异步校验任务 ----------
export type ValidateJobStartResponse = {
    job_id: string;
    total: number;
};

export type ValidateJobStatusResponse = {
    id: string;
    channel_id: number;
    model: string;
    state: 'pending' | 'running' | 'success' | 'error' | 'canceled';
    error?: string;
    total: number;
    tested: number;
    disabled: number;
    success: number;
    started_at?: number;
    updated_at?: number;
    timeout_sec?: number;
};

export async function startValidateChannelKeys(data: ValidateChannelKeysRequest) {
    return apiClient.post<ValidateJobStartResponse>('/api/v1/channel/keys/validate/start', data);
}

export async function getValidateJobStatus(id: string) {
    return apiClient.get<ValidateJobStatusResponse>(`/api/v1/channel/keys/validate/status?id=${encodeURIComponent(id)}`);
}

export async function cancelValidateJob(id: string) {
    return apiClient.post<{ canceled: boolean }>('/api/v1/channel/keys/validate/cancel', { id });
}

/**
 * 导入渠道密钥 Hook
 */
export function useImportChannelKeys() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (data: { channel_id: number; text: string }) => {
            return apiClient.post<{ added: number; skipped: number }>('/api/v1/channel/keys/import', data);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
        },
    });
}

/**
 * 批量验证渠道密钥池
 */
export function useValidateChannelKeys() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (data: ValidateChannelKeysRequest) => {
            return apiClient.post<ValidateChannelKeysResponse>('/api/v1/channel/keys/validate', data);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channel', 'keys'] });
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
        },
    });
}

/**
 * 恢复禁用密钥 Hook
 */
export function useRestoreInvalidChannelKeys() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (data: { channel_id: number }) => {
            return apiClient.post<{ restored: number }>('/api/v1/channel/keys/restore-invalid', data);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
        },
    });
}

/**
 * 清空禁用密钥 Hook
 */
export function useClearInvalidChannelKeys() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (data: { channel_id: number }) => {
            return apiClient.post<{ cleared: number }>('/api/v1/channel/keys/clear-invalid', data);
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
        },
    });
}

/**
 * 删除渠道所有禁用密钥 Hook
 *
 * @example
 * const deleteDisabledKeys = useDeleteDisabledKeys();
 *
 * deleteDisabledKeys.mutate(channelId);
 */
export function useDeleteDisabledKeys() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (channelId: number) => {
            return apiClient.delete<{ deleted: number }>(`/api/v1/channel/keys/delete-disabled/${channelId}`);
        },
        onSuccess: () => {
            logger.log('禁用密钥删除成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['channel', 'keys'] });
        },
        onError: (error) => {
            logger.error('禁用密钥删除失败:', error);
        },
    });
}

export type ChannelKeyPage = {
    items: ChannelKey[];
    total: number;
    page: number;
    page_size: number;
};

/**
 * 分页获取渠道密钥
 */
export function useChannelKeys(params: { channel_id?: number; page: number; page_size: number; enabled?: boolean }) {
    const { channel_id, page, page_size, enabled } = params;
    return useQuery({
        enabled: !!channel_id,
        queryKey: ['channel', 'keys', channel_id, page, page_size, enabled],
        queryFn: async () => {
            return apiClient.post<ChannelKeyPage>('/api/v1/channel/keys/list', {
                channel_id,
                page,
                page_size,
                enabled,
            });
        },
        staleTime: 10 * 1000,
    });
}

/**
 * 测试渠道 Hook
 *
 * @example
 * const testChannel = useTestChannel();
 *
 * testChannel.mutate({
 *   channel_id: 1,
 *   model: 'gpt-4',
 * });
 */
export function useTestChannel() {
    return useMutation({
        mutationFn: async (data: TestChannelRequest) => {
            return apiClient.post<TestChannelResponse>('/api/v1/channel/test', data);
        },
        onSuccess: (data) => {
            logger.log('渠道测试完成:', data);
        },
        onError: (error) => {
            logger.error('渠道测试失败:', error);
        },
    });
}
