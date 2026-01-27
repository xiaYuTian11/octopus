package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
	"gorm.io/gorm/clause"
)

var channelCache = cache.New[int, model.Channel](16)
var channelKeyCache = cache.New[int, model.ChannelKey](16)
var channelKeyCacheNeedUpdate = make(map[int]struct{})
var channelKeyCacheNeedUpdateLock sync.Mutex
var channelCacheLock sync.Mutex // 保护 channelCache 的并发访问

// 速率限制相关
// keyRateLimitCache 存储每个 Key 的请求时间戳列表（滑动窗口）
// key: ChannelKey.ID, value: 请求时间戳列表
var keyRateLimitCache = cache.New[int, *rateLimitWindow](16)
var keyRateLimitLock sync.Mutex

// MaxRateLimitWaitDuration 最大等待时间，当所有 Key 都达到速率限制时，最多等待这么长时间
var MaxRateLimitWaitDuration = 60 * time.Second

// ErrRateLimitExceeded 表示速率限制已达到
var ErrRateLimitExceeded = errors.New("rate limit exceeded")

// ErrAllKeysRateLimited 表示所有 Key 都达到了速率限制
var ErrAllKeysRateLimited = errors.New("all keys have reached rate limit")

// ErrRateLimitWaitTimeout 表示等待速率限制恢复超时
var ErrRateLimitWaitTimeout = errors.New("rate limit wait timeout exceeded")

// rateLimitWindow 滑动窗口速率限制器
type rateLimitWindow struct {
	mu         sync.Mutex
	timestamps []int64 // 请求时间戳列表（Unix 秒）
	windowSec  int64   // 窗口大小（秒）
}

// newRateLimitWindow 创建新的滑动窗口
func newRateLimitWindow(windowSec int64) *rateLimitWindow {
	return &rateLimitWindow{
		timestamps: make([]int64, 0),
		windowSec:  windowSec,
	}
}

// tryAcquire 尝试获取一个请求配额
// 返回 true 表示成功获取，false 表示已达到限制
func (w *rateLimitWindow) tryAcquire(limit int) bool {
	if limit <= 0 {
		return true // 不限制
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().Unix()
	windowStart := now - w.windowSec

	// 清理过期的时间戳
	validIdx := 0
	for _, ts := range w.timestamps {
		if ts > windowStart {
			w.timestamps[validIdx] = ts
			validIdx++
		}
	}
	w.timestamps = w.timestamps[:validIdx]

	// 检查是否超过限制
	if len(w.timestamps) >= limit {
		return false
	}

	// 添加当前时间戳
	w.timestamps = append(w.timestamps, now)
	return true
}

// count 返回当前窗口内的请求数
func (w *rateLimitWindow) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().Unix()
	windowStart := now - w.windowSec

	count := 0
	for _, ts := range w.timestamps {
		if ts > windowStart {
			count++
		}
	}
	return count
}

// getNextAvailableTime 获取下一个可用时间点
// 返回最早的请求时间戳 + 窗口大小，即最早的请求过期的时间
// 如果当前未达到限制，返回当前时间
func (w *rateLimitWindow) getNextAvailableTime(limit int) time.Time {
	if limit <= 0 {
		return time.Now()
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().Unix()
	windowStart := now - w.windowSec

	// 收集有效的时间戳并排序
	validTimestamps := make([]int64, 0, len(w.timestamps))
	for _, ts := range w.timestamps {
		if ts > windowStart {
			validTimestamps = append(validTimestamps, ts)
		}
	}

	// 如果未达到限制，返回当前时间
	if len(validTimestamps) < limit {
		return time.Now()
	}

	// 找到最早的时间戳，它过期后就有一个配额可用
	minTs := validTimestamps[0]
	for _, ts := range validTimestamps[1:] {
		if ts < minTs {
			minTs = ts
		}
	}

	// 返回最早时间戳 + 窗口大小（滑动窗口判断是 ts > windowStart，所以不需要 +1）
	return time.Unix(minTs+w.windowSec, 0)
}

// increment 增加请求计数（用于请求成功后调用）
func (w *rateLimitWindow) increment() {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().Unix()
	w.timestamps = append(w.timestamps, now)
}

func ChannelList(ctx context.Context) ([]model.Channel, error) {
	channels := make([]model.Channel, 0, channelCache.Len())

	// 预聚合 key 统计，避免逐渠道 count.
	type keyAgg struct {
		ChannelID int64
		Total     int64
		Enabled   int64
	}
	keyAggs := []keyAgg{}
	_ = db.GetDB().WithContext(ctx).
		Model(&model.ChannelKey{}).
		Select("channel_id, COUNT(*) as total, SUM(CASE WHEN enabled THEN 1 ELSE 0 END) as enabled").
		Group("channel_id").
		Find(&keyAggs).Error
	keyMap := make(map[int64]keyAgg, len(keyAggs))
	for _, ka := range keyAggs {
		keyMap[ka.ChannelID] = ka
	}

	for _, channel := range channelCache.GetAll() {
		ch := channel
		if agg, ok := keyMap[int64(ch.ID)]; ok {
			setKeyCountsFromAgg(&ch, agg.Total, agg.Enabled)
		} else {
			setKeyCountsFromKeys(&ch)
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

func ChannelCreate(channel *model.Channel, ctx context.Context) error {
	if channel.KeyPoolEnabled && channel.KeyFailThreshold <= 0 {
		channel.KeyFailThreshold = 3
	}
	setKeyCountsFromKeys(channel)
	if err := db.GetDB().WithContext(ctx).Create(channel).Error; err != nil {
		return err
	}
	channelCache.Set(channel.ID, *channel)
	for _, k := range channel.Keys {
		if k.ID != 0 {
			channelKeyCache.Set(k.ID, k)
		}
	}
	return nil
}

// ChannelSelectKey chooses a key depending on channel config.
// For pool mode, it queries DB directly to avoid loading massive key lists into memory.
// For legacy mode, it keeps existing in-memory selection.
// 现在增加了速率限制检查，当所有 Key 都达到限制时会等待而不是直接返回错误。
func ChannelSelectKey(ctx context.Context, ch *model.Channel) (model.ChannelKey, error) {
	if ch == nil {
		return model.ChannelKey{}, fmt.Errorf("channel is nil")
	}

	startTime := time.Now()
	maxWait := MaxRateLimitWaitDuration

	for {
		// 检查是否超过最大等待时间
		if time.Since(startTime) > maxWait {
			log.Warnf("channel %d: rate limit wait timeout after %v", ch.ID, maxWait)
			return model.ChannelKey{}, ErrRateLimitWaitTimeout
		}

		// 检查 context 是否已取消
		select {
		case <-ctx.Done():
			return model.ChannelKey{}, ctx.Err()
		default:
		}

		key, waitDuration, err := trySelectKeyWithRateLimit(ctx, ch)
		if err == nil {
			return key, nil
		}

		if !errors.Is(err, ErrAllKeysRateLimited) {
			// 其他错误直接返回
			return model.ChannelKey{}, err
		}

		// 所有 key 都达到了速率限制，需要等待
		if waitDuration <= 0 {
			waitDuration = time.Second // 最小等待 1 秒
		}

		// 确保不会等待超过剩余的最大等待时间
		remainingWait := maxWait - time.Since(startTime)
		if waitDuration > remainingWait {
			if remainingWait <= 0 {
				log.Warnf("channel %d: rate limit wait timeout after %v", ch.ID, maxWait)
				return model.ChannelKey{}, ErrRateLimitWaitTimeout
			}
			waitDuration = remainingWait
		}

		log.Infof("channel %d: all keys rate limited, waiting %v before retry", ch.ID, waitDuration)

		// 使用 select 等待，支持 context 取消
		select {
		case <-ctx.Done():
			return model.ChannelKey{}, ctx.Err()
		case <-time.After(waitDuration):
			// 继续重试
		}
	}
}

// trySelectKeyWithRateLimit 尝试选择一个未达到速率限制的 key
// 返回值：选中的 key，需要等待的时间（如果所有 key 都达到限制），错误
func trySelectKeyWithRateLimit(ctx context.Context, ch *model.Channel) (model.ChannelKey, time.Duration, error) {
	if !ch.KeyPoolEnabled {
		return trySelectKeyFromMemory(ch)
	}
	return trySelectKeyFromDB(ctx, ch)
}

// trySelectKeyFromMemory 从内存中选择 key（非 KeyPool 模式）
func trySelectKeyFromMemory(ch *model.Channel) (model.ChannelKey, time.Duration, error) {
	keys := ch.GetAvailableKeys()
	if len(keys) == 0 {
		return model.ChannelKey{}, 0, fmt.Errorf("no available key")
	}

	var minWaitTime time.Duration
	now := time.Now()

	for _, key := range keys {
		if key.RateLimitRPM <= 0 {
			// 不限制速率，直接返回
			return key, 0, nil
		}
		if checkKeyRateLimit(key.ID, key.RateLimitRPM) {
			return key, 0, nil
		}

		// 计算这个 key 的下一个可用时间
		waitTime := getKeyNextAvailableWait(key.ID, key.RateLimitRPM, now)
		if minWaitTime == 0 || waitTime < minWaitTime {
			minWaitTime = waitTime
		}

		log.Debugf("key %d rate limit exceeded (%d RPM), next available in %v", key.ID, key.RateLimitRPM, waitTime)
	}

	// 所有 key 都达到了速率限制
	log.Debugf("all keys for channel %d have reached rate limit, min wait: %v", ch.ID, minWaitTime)
	return model.ChannelKey{}, minWaitTime, ErrAllKeysRateLimited
}

// trySelectKeyFromDB 从数据库中选择 key（KeyPool 模式）
func trySelectKeyFromDB(ctx context.Context, ch *model.Channel) (model.ChannelKey, time.Duration, error) {
	now := time.Now()
	nowUnix := now.Unix()
	cooldownSec := int64(5 * time.Minute / time.Second)

	// 获取所有可用的 key，然后逐个检查速率限制
	var keys []model.ChannelKey
	q := db.GetDB().WithContext(ctx).
		Model(&model.ChannelKey{}).
		Where("channel_id = ? AND enabled = ? AND channel_key <> ''", ch.ID, true).
		// 排除明显占位/无效 key，例如长度 < 8
		Where("LENGTH(channel_key) >= 8").
		Where("(status_code != 429 OR last_use_time_stamp = 0 OR ? - last_use_time_stamp >= ?)", nowUnix, cooldownSec).
		Order("last_use_time_stamp ASC").
		Order("id ASC").
		Limit(100) // 限制查询数量，避免大量 key 时性能问题

	if err := q.Find(&keys).Error; err != nil {
		return model.ChannelKey{}, 0, err
	}

	if len(keys) == 0 {
		return model.ChannelKey{}, 0, fmt.Errorf("no available key")
	}

	var minWaitTime time.Duration

	// 遍历 key，找到第一个未达到速率限制的
	for _, key := range keys {
		if key.RateLimitRPM <= 0 {
			// 不限制速率，直接返回
			return key, 0, nil
		}
		if checkKeyRateLimit(key.ID, key.RateLimitRPM) {
			return key, 0, nil
		}

		// 计算这个 key 的下一个可用时间
		waitTime := getKeyNextAvailableWait(key.ID, key.RateLimitRPM, now)
		if minWaitTime == 0 || waitTime < minWaitTime {
			minWaitTime = waitTime
		}

		log.Debugf("key %d rate limit exceeded (%d RPM), next available in %v", key.ID, key.RateLimitRPM, waitTime)
	}

	// 所有 key 都达到了速率限制
	log.Debugf("all keys for channel %d have reached rate limit, min wait: %v", ch.ID, minWaitTime)
	return model.ChannelKey{}, minWaitTime, ErrAllKeysRateLimited
}

// getKeyNextAvailableWait 获取 key 下一个可用时间距离现在的等待时间
func getKeyNextAvailableWait(keyID int, limitRPM int, now time.Time) time.Duration {
	keyRateLimitLock.Lock()
	window, ok := keyRateLimitCache.Get(keyID)
	if !ok {
		keyRateLimitLock.Unlock()
		return 0
	}
	keyRateLimitLock.Unlock()

	nextAvailable := window.getNextAvailableTime(limitRPM)
	waitTime := nextAvailable.Sub(now)
	if waitTime < 0 {
		return 0
	}
	return waitTime
}

// checkKeyRateLimit 检查 key 是否达到速率限制
// 返回 true 表示未达到限制，可以使用
func checkKeyRateLimit(keyID int, limitRPM int) bool {
	if limitRPM <= 0 {
		return true
	}

	keyRateLimitLock.Lock()
	window, ok := keyRateLimitCache.Get(keyID)
	if !ok {
		window = newRateLimitWindow(60) // 1 分钟窗口
		keyRateLimitCache.Set(keyID, window)
	}
	keyRateLimitLock.Unlock()

	return window.tryAcquire(limitRPM)
}

// ChannelKeyUpdateImmediate updates caches and persists immediately.
// Use for pool mode so that failure counts / disabled flags take effect without waiting for cache flush.
func ChannelKeyUpdateImmediate(ctx context.Context, key model.ChannelKey) error {
	if err := ChannelKeyUpdate(key); err != nil {
		return err
	}
	return db.GetDB().WithContext(ctx).Save(&key).Error
}

// ChannelKeyUpdate 仅更新 ChannelKey 的内存缓存（不落库），并标记为需要在 SaveCache 时写入数据库。
// 使用锁保护缓存更新过程，确保原子性
func ChannelKeyUpdate(key model.ChannelKey) error {
	if key.ID == 0 || key.ChannelID == 0 {
		return fmt.Errorf("invalid channel key")
	}

	// 加锁保护整个缓存更新过程，防止 read-modify-write 竞态
	channelCacheLock.Lock()
	defer channelCacheLock.Unlock()

	ch, ok := channelCache.Get(key.ChannelID)
	if !ok {
		return fmt.Errorf("channel not found")
	}

	// 创建深拷贝以避免修改共享数据
	if len(ch.Keys) > 0 {
		keys := make([]model.ChannelKey, len(ch.Keys))
		copy(keys, ch.Keys)
		keyFound := false
		for i := range keys {
			if keys[i].ID == key.ID {
				keys[i] = key
				keyFound = true
				break
			}
		}
		if !keyFound {
			return fmt.Errorf("channel key %d not found in channel %d", key.ID, key.ChannelID)
		}
		ch.Keys = keys
	}

	// 原子性地更新所有相关缓存
	channelCache.Set(key.ChannelID, ch)
	channelKeyCache.Set(key.ID, key)

	// 标记需要更新到数据库
	channelKeyCacheNeedUpdateLock.Lock()
	channelKeyCacheNeedUpdate[key.ID] = struct{}{}
	channelKeyCacheNeedUpdateLock.Unlock()

	return nil
}
func ChannelBaseUrlUpdate(channelID int, baseUrl []model.BaseUrl) error {
	ch, ok := channelCache.Get(channelID)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	// Copy to decouple callers from internal cache storage.
	if baseUrl == nil {
		ch.BaseUrls = nil
	} else {
		cp := make([]model.BaseUrl, len(baseUrl))
		copy(cp, baseUrl)
		ch.BaseUrls = cp
	}
	channelCache.Set(channelID, ch)
	return nil
}

// ChannelKeySaveDB 将运行时更新过的 ChannelKey 缓存写入数据库。
func ChannelKeySaveDB(ctx context.Context) error {
	channelKeyCacheNeedUpdateLock.Lock()
	keyIDs := make([]int, 0, len(channelKeyCacheNeedUpdate))
	for id := range channelKeyCacheNeedUpdate {
		keyIDs = append(keyIDs, id)
	}
	channelKeyCacheNeedUpdate = make(map[int]struct{})
	channelKeyCacheNeedUpdateLock.Unlock()

	if len(keyIDs) == 0 {
		return nil
	}

	dbConn := db.GetDB().WithContext(ctx)
	for _, id := range keyIDs {
		k, ok := channelKeyCache.Get(id)
		if !ok {
			continue
		}
		if err := dbConn.Save(&k).Error; err != nil {
			return err
		}
	}
	return nil
}

// ChannelKeysImport inserts keys in batches; returns added count and skipped duplicates count.
func ChannelKeysImport(ctx context.Context, channelID int, raw string) (added int64, skipped int64, err error) {
	if channelID <= 0 {
		return 0, 0, fmt.Errorf("invalid channel_id")
	}
	lines := strings.Split(raw, "\n")
	unique := make([]string, 0, len(lines))
	seen := make(map[string]struct{})
	for _, line := range lines {
		k := strings.TrimSpace(line)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		unique = append(unique, k)
	}
	if len(unique) == 0 {
		return 0, 0, nil
	}

	dbConn := db.GetDB().WithContext(ctx)
	// 先查出数据库已有的 key，避免没有唯一索引时重复插入。
	existing := make(map[string]struct{})
	inBatch := 5000
	for start := 0; start < len(unique); start += inBatch {
		end := start + inBatch
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		var found []string
		if err := dbConn.Model(&model.ChannelKey{}).
			Where("channel_id = ? AND channel_key IN ?", channelID, chunk).
			Pluck("channel_key", &found).Error; err != nil {
			return 0, 0, err
		}
		for _, f := range found {
			existing[f] = struct{}{}
		}
	}

	keys := make([]model.ChannelKey, 0, len(unique))
	for _, k := range unique {
		if _, ok := existing[k]; ok {
			continue
		}
		keys = append(keys, model.ChannelKey{
			ChannelID:  channelID,
			Enabled:    true,
			ChannelKey: k,
		})
	}
	// 数据库已有的算作 skipped
	skipped = int64(len(unique) - len(keys))

	if len(keys) == 0 {
		return 0, skipped, nil
	}

	batch := 5000
	for start := 0; start < len(keys); start += batch {
		end := start + batch
		if end > len(keys) {
			end = len(keys)
		}
		batchKeys := keys[start:end]
		result := dbConn.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "channel_id"}, {Name: "channel_key"}},
			DoNothing: true,
		}).Create(&batchKeys)
		if result.Error != nil {
			return added, skipped, result.Error
		}
		// affected rows = inserted; skipped = duplicates
		added += result.RowsAffected
		skipped += int64(len(batchKeys)) - result.RowsAffected
	}
	// refresh cache for the channel
	_ = channelRefreshCacheByID(channelID, ctx)
	return added, skipped, nil
}

func ChannelKeysRestoreInvalid(ctx context.Context, channelID int) (int64, error) {
	if channelID <= 0 {
		return 0, fmt.Errorf("invalid channel_id")
	}
	dbConn := db.GetDB().WithContext(ctx)
	res := dbConn.Model(&model.ChannelKey{}).
		Where("channel_id = ? AND enabled = ? AND failure_count > 0", channelID, false).
		Updates(map[string]interface{}{
			"enabled":         true,
			"failure_count":   0,
			"status_code":     0,
			"disabled_reason": "",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	_ = channelRefreshCacheByID(channelID, ctx)
	return res.RowsAffected, nil
}

func ChannelKeysClearInvalid(ctx context.Context, channelID int) (int64, error) {
	if channelID <= 0 {
		return 0, fmt.Errorf("invalid channel_id")
	}
	dbConn := db.GetDB().WithContext(ctx)
	res := dbConn.Where("channel_id = ? AND enabled = ?", channelID, false).Delete(&model.ChannelKey{})
	if res.Error != nil {
		return 0, res.Error
	}
	_ = channelRefreshCacheByID(channelID, ctx)
	return res.RowsAffected, nil
}

func ChannelUpdate(req *model.ChannelUpdateRequest, ctx context.Context) (*model.Channel, error) {
	_, ok := channelCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("channel not found")
	}

	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var selectFields []string
	updates := model.Channel{ID: req.ID}

	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Type != nil {
		selectFields = append(selectFields, "type")
		updates.Type = *req.Type
	}
	if req.Enabled != nil {
		selectFields = append(selectFields, "enabled")
		updates.Enabled = *req.Enabled
	}
	if req.BaseUrls != nil {
		selectFields = append(selectFields, "base_urls")
		updates.BaseUrls = *req.BaseUrls
	}
	if req.Model != nil {
		selectFields = append(selectFields, "model")
		updates.Model = *req.Model
	}
	if req.CustomModel != nil {
		selectFields = append(selectFields, "custom_model")
		updates.CustomModel = *req.CustomModel
	}
	if req.Proxy != nil {
		selectFields = append(selectFields, "proxy")
		updates.Proxy = *req.Proxy
	}
	if req.AutoSync != nil {
		selectFields = append(selectFields, "auto_sync")
		updates.AutoSync = *req.AutoSync
	}
	if req.AutoGroup != nil {
		selectFields = append(selectFields, "auto_group")
		updates.AutoGroup = *req.AutoGroup
	}
	if req.KeyPoolEnabled != nil {
		selectFields = append(selectFields, "key_pool_enabled")
		updates.KeyPoolEnabled = *req.KeyPoolEnabled
	}
	if req.KeyFailThreshold != nil {
		threshold := *req.KeyFailThreshold
		if threshold <= 0 {
			threshold = 3
		}
		selectFields = append(selectFields, "key_fail_threshold")
		updates.KeyFailThreshold = threshold
	}
	if req.CustomHeader != nil {
		selectFields = append(selectFields, "custom_header")
		updates.CustomHeader = *req.CustomHeader
	}
	if req.ChannelProxy != nil {
		selectFields = append(selectFields, "channel_proxy")
		updates.ChannelProxy = req.ChannelProxy
	}
	if req.ParamOverride != nil {
		selectFields = append(selectFields, "param_override")
		updates.ParamOverride = req.ParamOverride
	}

	// 只有当有字段需要更新时才执行 UPDATE
	if len(selectFields) > 0 {
		if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update channel: %w", err)
		}
	}

	// 删除 keys
	if len(req.KeysToDelete) > 0 {
		if err := tx.Where("id IN ? AND channel_id = ?", req.KeysToDelete, req.ID).Delete(&model.ChannelKey{}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to delete channel keys: %w", err)
		}
	}

	// 更新 keys（逐条，只更新提供的字段）
	if len(req.KeysToUpdate) > 0 {
		for _, ku := range req.KeysToUpdate {
			updates := map[string]interface{}{}
			if ku.Enabled != nil {
				updates["enabled"] = *ku.Enabled
			}
			if ku.ChannelKey != nil {
				updates["channel_key"] = *ku.ChannelKey
			}
			if ku.Remark != nil {
				updates["remark"] = *ku.Remark
			}
			if ku.RateLimitRPM != nil {
				updates["rate_limit_rpm"] = *ku.RateLimitRPM
			}
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&model.ChannelKey{}).
				Where("id = ? AND channel_id = ?", ku.ID, req.ID).
				Updates(updates).Error; err != nil {
				tx.Rollback()
				return nil, fmt.Errorf("failed to update channel key %d: %w", ku.ID, err)
			}
		}
	}

	// 新增 keys
	if len(req.KeysToAdd) > 0 {
		newKeys := make([]model.ChannelKey, 0, len(req.KeysToAdd))
		for _, ka := range req.KeysToAdd {
			newKeys = append(newKeys, model.ChannelKey{
				ChannelID:    req.ID,
				Enabled:      ka.Enabled,
				ChannelKey:   ka.ChannelKey,
				Remark:       ka.Remark,
				RateLimitRPM: ka.RateLimitRPM,
			})
		}
		if err := tx.Create(&newKeys).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create channel keys: %w", err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 刷新缓存并返回最新数据
	if err := channelRefreshCacheByID(req.ID, ctx); err != nil {
		return nil, err
	}

	channel, _ := channelCache.Get(req.ID)
	return &channel, nil
}

func ChannelEnabled(id int, enabled bool, ctx context.Context) error {
	oldChannel, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return err
	}
	oldChannel.Enabled = enabled
	channelCache.Set(id, oldChannel)
	return nil
}

func ChannelDel(id int, ctx context.Context) error {
	ch, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("channel not found")
	}

	// 开启事务
	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 获取所有受影响的 GroupID，用于刷新缓存
	var affectedGroupIDs []int
	if err := tx.Model(&model.GroupItem{}).
		Where("channel_id = ?", id).
		Pluck("group_id", &affectedGroupIDs).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to get affected groups: %w", err)
	}

	// 删除所有引用该渠道的 GroupItem
	if err := tx.Where("channel_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	// 删除渠道 keys
	if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelKey{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel keys: %w", err)
	}

	// 删除统计数据
	if err := tx.Where("channel_id = ?", id).Delete(&model.StatsChannel{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel stats: %w", err)
	}

	// 删除渠道
	if err := tx.Delete(&model.Channel{}, id).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel: %w", err)
	}

	// 提交事务
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 删除缓存
	channelCache.Del(id)
	for _, k := range ch.Keys {
		if k.ID != 0 {
			channelKeyCache.Del(k.ID)
		}
	}
	StatsChannelDel(id)

	// 刷新受影响的分组缓存
	// 收集所有刷新错误，但不影响删除操作的成功
	var refreshErrors []error
	for _, groupID := range affectedGroupIDs {
		if err := groupRefreshCacheByID(groupID, ctx); err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("group %d: %w", groupID, err))
			log.Warnf("failed to refresh group cache for group %d: %v", groupID, err)
		}
	}

	// 如果有刷新错误，记录但不返回错误（因为删除操作已成功）
	if len(refreshErrors) > 0 {
		log.Warnf("channel %d deleted successfully, but some group caches failed to refresh: %v", id, refreshErrors)
	}

	return nil
}

func ChannelLLMList(ctx context.Context) ([]model.LLMChannel, error) {
	models := []model.LLMChannel{}
	for _, channel := range channelCache.GetAll() {
		modelNames := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
		for _, modelName := range modelNames {
			if modelName == "" {
				continue
			}
			models = append(models, model.LLMChannel{
				Name:        modelName,
				Enabled:     channel.Enabled,
				ChannelID:   channel.ID,
				ChannelName: channel.Name,
			})
		}
	}
	return models, nil
}

func ChannelGet(id int, ctx context.Context) (*model.Channel, error) {
	channel, ok := channelCache.Get(id)
	if !ok {
		return nil, fmt.Errorf("channel not found")
	}
	return &channel, nil
}

func channelRefreshCache(ctx context.Context) error {
	channels := []model.Channel{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		Preload("Stats").
		Find(&channels).Error; err != nil {
		log.Warnf("failed to get channels: %v", err)
		return err
	}
	channelKeyCache.Clear()
	channelKeyCacheNeedUpdateLock.Lock()
	channelKeyCacheNeedUpdate = make(map[int]struct{})
	channelKeyCacheNeedUpdateLock.Unlock()
	for _, channel := range channels {
		channelCache.Set(channel.ID, channel)
		for _, k := range channel.Keys {
			if k.ID != 0 {
				channelKeyCache.Set(k.ID, k)
			}
		}
	}
	return nil
}

func channelRefreshCacheByID(id int, ctx context.Context) error {
	if old, ok := channelCache.Get(id); ok {
		for _, k := range old.Keys {
			if k.ID != 0 {
				channelKeyCache.Del(k.ID)
			}
		}
	}
	var channel model.Channel
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		Preload("Stats").
		First(&channel, id).Error; err != nil {
		return err
	}
	setKeyCountsFromKeys(&channel)
	channelCache.Set(channel.ID, channel)
	for _, k := range channel.Keys {
		if k.ID != 0 {
			channelKeyCache.Set(k.ID, k)
		}
	}
	return nil
}

func setKeyCountsFromKeys(ch *model.Channel) {
	total := len(ch.Keys)
	enabled := 0
	for _, k := range ch.Keys {
		if k.Enabled {
			enabled++
		}
	}
	ch.KeyCount = total
	ch.KeyEnabledCount = enabled
	ch.KeyDisabledCount = total - enabled
}

func setKeyCountsFromAgg(ch *model.Channel, total int64, enabled int64) {
	ch.KeyCount = int(total)
	ch.KeyEnabledCount = int(enabled)
	ch.KeyDisabledCount = int(total - enabled)
}

// ChannelKeysCount 返回渠道下的 key 总数，可选过滤 enabled 状态。
func ChannelKeysCount(ctx context.Context, channelID int, enabled *bool) (int64, error) {
	if channelID <= 0 {
		return 0, fmt.Errorf("invalid channel_id")
	}
	dbConn := db.GetDB().WithContext(ctx)
	query := dbConn.Model(&model.ChannelKey{}).Where("channel_id = ?", channelID)
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// ChannelKeysPage 返回分页后的密钥列表及总数，用于密钥池查看。
func ChannelKeysPage(ctx context.Context, channelID int, page, pageSize int, enabled *bool) ([]model.ChannelKey, int64, error) {
	if channelID <= 0 {
		return nil, 0, fmt.Errorf("invalid channel_id")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	dbConn := db.GetDB().WithContext(ctx)
	query := dbConn.Model(&model.ChannelKey{}).Where("channel_id = ?", channelID)
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	keys := []model.ChannelKey{}
	if err := query.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&keys).Error; err != nil {
		return nil, 0, err
	}
	return keys, total, nil
}
