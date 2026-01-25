package op

import (
	"context"
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

func ChannelList(ctx context.Context) ([]model.Channel, error) {
	channels := make([]model.Channel, 0, channelCache.Len())
	for _, channel := range channelCache.GetAll() {
		channels = append(channels, channel)
	}
	return channels, nil
}

func ChannelCreate(channel *model.Channel, ctx context.Context) error {
	if channel.KeyPoolEnabled && channel.KeyFailThreshold <= 0 {
		channel.KeyFailThreshold = 3
	}
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
func ChannelSelectKey(ctx context.Context, ch *model.Channel) (model.ChannelKey, error) {
	if ch == nil {
		return model.ChannelKey{}, fmt.Errorf("channel is nil")
	}
	if !ch.KeyPoolEnabled {
		return ch.GetChannelKey(), nil
	}

	now := time.Now().Unix()
	cooldownSec := int64(5 * time.Minute / time.Second)

	var key model.ChannelKey
	q := db.GetDB().WithContext(ctx).
		Model(&model.ChannelKey{}).
		Where("channel_id = ? AND enabled = ? AND channel_key <> ''", ch.ID, true).
		Where("(status_code != 429 OR last_use_time_stamp = 0 OR ? - last_use_time_stamp >= ?)", now, cooldownSec).
		Order("last_use_time_stamp ASC").
		Order("id ASC")

	if err := q.First(&key).Error; err != nil {
		return model.ChannelKey{}, err
	}
	return key, nil
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
	keys := make([]model.ChannelKey, 0, len(lines))
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
		keys = append(keys, model.ChannelKey{
			ChannelID:  channelID,
			Enabled:    true,
			ChannelKey: k,
		})
	}
	if len(keys) == 0 {
		return 0, 0, nil
	}

	batch := 5000
	dbConn := db.GetDB().WithContext(ctx)
	for start := 0; start < len(keys); start += batch {
		end := start + batch
		if end > len(keys) {
			end = len(keys)
		}
		batchKeys := keys[start:end]
		if err := dbConn.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "channel_id"}, {Name: "channel_key"}},
			DoNothing: true,
		}).Create(&batchKeys).Error; err != nil {
			return added, skipped, err
		}
		// affected rows = inserted; skipped = duplicates
		added += dbConn.RowsAffected
		skipped += int64(len(batchKeys)) - dbConn.RowsAffected
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
				ChannelID:  req.ID,
				Enabled:    ka.Enabled,
				ChannelKey: ka.ChannelKey,
				Remark:     ka.Remark,
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
	channelCache.Set(channel.ID, channel)
	for _, k := range channel.Keys {
		if k.ID != 0 {
			channelKeyCache.Set(k.ID, k)
		}
	}
	return nil
}
