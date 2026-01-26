package op

import (
	"context"
	"fmt"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	groupCache  = cache.New[int, model.Group](16)
	groupMap    = cache.New[string, model.Group](16)
	groupTagMap = cache.New[string, model.Group](4)
)

func GroupList(ctx context.Context) ([]model.Group, error) {
	groups := make([]model.Group, 0, groupCache.Len())
	for _, group := range groupCache.GetAll() {
		groups = append(groups, group)
	}
	return groups, nil
}

func GroupListModel(ctx context.Context) ([]string, error) {
	models := []string{}
	for _, group := range groupCache.GetAll() {
		models = append(models, group.Name)
	}
	return models, nil
}

func GroupGet(id int, ctx context.Context) (*model.Group, error) {
	group, ok := groupCache.Get(id)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	return &group, nil
}

func GroupGetMap(name string, ctx context.Context) (model.Group, error) {
	items, ok := groupMap.Get(name)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	return items, nil
}

func GroupGetByTag(tag string, ctx context.Context) (model.Group, error) {
	if tag == "" {
		return model.Group{}, fmt.Errorf("group not found")
	}
	group, ok := groupTagMap.Get(tag)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	return group, nil
}

func GroupCreate(group *model.Group, ctx context.Context) error {
	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if group.Tag != "" {
		if err := clearGroupTagTx(tx, group.Tag, 0, ctx); err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := tx.Create(group).Error; err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Commit().Error; err != nil {
		return err
	}

	return groupRefreshCacheByID(group.ID, ctx)
}

func GroupUpdate(req *model.GroupUpdateRequest, ctx context.Context) (*model.Group, error) {
	oldGroup, ok := groupCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	oldName := oldGroup.Name

	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var selectFields []string
	updates := model.Group{ID: req.ID}

	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Mode != nil {
		selectFields = append(selectFields, "mode")
		updates.Mode = *req.Mode
	}
	if req.MatchRegex != nil {
		selectFields = append(selectFields, "match_regex")
		updates.MatchRegex = *req.MatchRegex
	}
	if req.FirstTokenTimeOut != nil {
		selectFields = append(selectFields, "first_token_time_out")
		updates.FirstTokenTimeOut = *req.FirstTokenTimeOut
	}
	if req.Tag != nil {
		selectFields = append(selectFields, "tag")
		updates.Tag = *req.Tag
		if err := clearGroupTagTx(tx, *req.Tag, req.ID, ctx); err != nil {
			tx.Rollback()
			return nil, err
		}
	}

	if len(selectFields) > 0 {
		if err := tx.Model(&model.Group{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update group: %w", err)
		}
	}

	// 鍒犻櫎 items
	if len(req.ItemsToDelete) > 0 {
		if err := tx.Where("id IN ? AND group_id = ?", req.ItemsToDelete, req.ID).Delete(&model.GroupItem{}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to delete items: %w", err)
		}
	}

	// 鎵归噺鏇存柊 items
	if len(req.ItemsToUpdate) > 0 {
		ids := make([]int, len(req.ItemsToUpdate))
		priorityCase := "CASE id"
		weightCase := "CASE id"
		for i, item := range req.ItemsToUpdate {
			ids[i] = item.ID
			priorityCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Priority)
			weightCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Weight)
		}
		priorityCase += " END"
		weightCase += " END"

		if err := tx.Model(&model.GroupItem{}).
			Where("id IN ? AND group_id = ?", ids, req.ID).
			Updates(map[string]interface{}{
				"priority": gorm.Expr(priorityCase),
				"weight":   gorm.Expr(weightCase),
			}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update items: %w", err)
		}
	}

	// 鎵归噺鏂板 items
	if len(req.ItemsToAdd) > 0 {
		newItems := make([]model.GroupItem, len(req.ItemsToAdd))
		for i, item := range req.ItemsToAdd {
			newItems[i] = model.GroupItem{
				GroupID:   req.ID,
				ChannelID: item.ChannelID,
				ModelName: item.ModelName,
				Priority:  item.Priority,
				Weight:    item.Weight,
			}
		}
		if err := tx.Create(&newItems).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create items: %w", err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 鍒锋柊缂撳瓨骞惰繑鍥炴渶鏂版暟鎹?
	if err := groupRefreshCacheByID(req.ID, ctx); err != nil {
		return nil, err
	}

	// 鑾峰彇鏇存柊鍚庣殑鍒嗙粍淇℃伅
	group, _ := groupCache.Get(req.ID)

	// 濡傛灉鍚嶇О鍙戠敓鍙樻洿锛岄渶瑕佸垹闄ゆ棫鍚嶇О鐨勬槧灏?
	// 娉ㄦ剰锛歡roupRefreshCacheByID 宸茬粡璁剧疆浜嗘柊鍚嶇О鐨勬槧灏?
	if req.Name != nil && *req.Name != oldName {
		groupMap.Del(oldName)
	}

	return &group, nil
}

func GroupDel(id int, ctx context.Context) error {
	group, ok := groupCache.Get(id)
	if !ok {
		return fmt.Errorf("group not found")
	}

	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err := tx.Where("group_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	if err := tx.Delete(&model.Group{}, id).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	groupCache.Del(id)
	groupMap.Del(group.Name)
	if group.Tag != "" {
		groupTagMap.Del(group.Tag)
	}
	return nil
}

func GroupItemAdd(item *model.GroupItem, ctx context.Context) error {
	if _, ok := groupCache.Get(item.GroupID); !ok {
		return fmt.Errorf("group not found")
	}

	if err := db.GetDB().WithContext(ctx).Create(item).Error; err != nil {
		return err
	}

	return groupRefreshCacheByID(item.GroupID, ctx)
}

func GroupItemBatchAdd(groupID int, items []model.GroupIDAndLLMName, ctx context.Context) error {
	if len(items) == 0 {
		return nil
	}

	group, ok := groupCache.Get(groupID)
	if !ok {
		return fmt.Errorf("group not found")
	}

	seen := make(map[string]struct{}, len(items))
	uniq := make([]model.GroupIDAndLLMName, 0, len(items))
	for _, it := range items {
		if it.ChannelID == 0 || it.ModelName == "" {
			continue
		}
		k := fmt.Sprintf("%d|%s", it.ChannelID, it.ModelName)
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, it)
	}
	if len(uniq) == 0 {
		return nil
	}

	nextPriority := 1
	for _, gi := range group.Items {
		if gi.Priority >= nextPriority {
			nextPriority = gi.Priority + 1
		}
	}

	newItems := make([]model.GroupItem, 0, len(uniq))
	for _, it := range uniq {
		newItems = append(newItems, model.GroupItem{
			GroupID:   groupID,
			ChannelID: it.ChannelID,
			ModelName: it.ModelName,
			Priority:  nextPriority,
			Weight:    1,
		})
		nextPriority++
	}

	if err := db.GetDB().WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "group_id"}, {Name: "channel_id"}, {Name: "model_name"}},
			DoNothing: true,
		}).
		Create(&newItems).Error; err != nil {
		return fmt.Errorf("failed to create group items: %w", err)
	}

	return groupRefreshCacheByID(groupID, ctx)
}

func GroupItemUpdate(item *model.GroupItem, ctx context.Context) error {
	if err := db.GetDB().WithContext(ctx).Model(item).
		Select("ModelName", "Priority", "Weight").
		Updates(item).Error; err != nil {
		return err
	}

	return groupRefreshCacheByID(item.GroupID, ctx)
}

func GroupItemDel(id int, ctx context.Context) error {
	var item model.GroupItem
	if err := db.GetDB().WithContext(ctx).First(&item, id).Error; err != nil {
		return fmt.Errorf("group item not found")
	}

	if err := db.GetDB().WithContext(ctx).Delete(&item).Error; err != nil {
		return err
	}

	return groupRefreshCacheByID(item.GroupID, ctx)
}

// GroupItemBatchDelByChannelAndModels 鏍规嵁娓犻亾ID鍜屾ā鍨嬪悕绉版壒閲忓垹闄ゅ垎缁勯」
func GroupItemBatchDelByChannelAndModels(keys []model.GroupIDAndLLMName, ctx context.Context) error {
	if len(keys) == 0 {
		return nil
	}

	conditions := make([][]interface{}, len(keys))
	for i, key := range keys {
		conditions[i] = []interface{}{key.ChannelID, key.ModelName}
	}

	var groupIDs []int
	if err := db.GetDB().WithContext(ctx).
		Model(&model.GroupItem{}).
		Distinct("group_id").
		Where("(channel_id, model_name) IN ?", conditions).
		Pluck("group_id", &groupIDs).Error; err != nil {
		return fmt.Errorf("failed to find group ids: %w", err)
	}

	if len(groupIDs) == 0 {
		return nil
	}

	if err := db.GetDB().WithContext(ctx).
		Where("(channel_id, model_name) IN ?", conditions).
		Delete(&model.GroupItem{}).Error; err != nil {
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	if err := groupRefreshCacheByIDs(groupIDs, ctx); err != nil {
		return fmt.Errorf("failed to refresh group cache: %w", err)
	}

	return nil
}

func GroupItemList(groupID int, ctx context.Context) ([]model.GroupItem, error) {
	var items []model.GroupItem
	if err := db.GetDB().WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("priority ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func groupRefreshCache(ctx context.Context) error {
	groups := []model.Group{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Find(&groups).Error; err != nil {
		return err
	}
	groupTagMap.Clear()
	for _, group := range groups {
		setGroupCache(group)
	}
	return nil
}

func groupRefreshCacheByID(id int, ctx context.Context) error {
	var group model.Group
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		First(&group, id).Error; err != nil {
		return err
	}
	setGroupCache(group)
	return nil
}

func groupRefreshCacheByIDs(ids []int, ctx context.Context) error {
	if len(ids) == 0 {
		return nil
	}
	var groups []model.Group
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Where("id IN ?", ids).
		Find(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		setGroupCache(group)
	}
	return nil
}

func setGroupCache(group model.Group) {
	groupCache.Set(group.ID, group)
	groupMap.Set(group.Name, group)
	removeGroupIDFromTagMap(group.ID)
	if group.Tag != "" {
		groupTagMap.Set(group.Tag, group)
	}
}

func removeGroupIDFromTagMap(id int) {
	for tag, g := range groupTagMap.GetAll() {
		if g.ID == id {
			groupTagMap.Del(tag)
		}
	}
}

func clearGroupTagTx(tx *gorm.DB, tag string, excludeID int, ctx context.Context) error {
	if tag == "" {
		return nil
	}

	var ids []int
	query := tx.Model(&model.Group{}).Where("tag = ?", tag)
	if excludeID > 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("failed to query groups by tag: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}

	if err := query.Update("tag", "").Error; err != nil {
		return fmt.Errorf("failed to clear duplicate tag: %w", err)
	}

	if err := groupRefreshCacheByIDs(ids, ctx); err != nil {
		return err
	}
	groupTagMap.Del(tag)
	return nil
}
