package op

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

const defaultWatchDedupMinutes = 60

type ChannelModelWatchHit struct {
	Watch        model.ChannelModelWatch
	MatchedModel string
}

func ChannelModelWatchGet(ctx context.Context, id int) (*model.ChannelModelWatch, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid id")
	}
	var watch model.ChannelModelWatch
	if err := db.GetDB().WithContext(ctx).First(&watch, id).Error; err != nil {
		return nil, err
	}
	return &watch, nil
}

func ChannelModelWatchList(ctx context.Context, channelID *int) ([]model.ChannelModelWatch, error) {
	conn := db.GetDB().WithContext(ctx).Model(&model.ChannelModelWatch{})
	if channelID != nil && *channelID > 0 {
		conn = conn.Where("channel_id = ?", *channelID)
	}
	var items []model.ChannelModelWatch
	if err := conn.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func ChannelModelWatchCreate(ctx context.Context, watch *model.ChannelModelWatch) error {
	if watch == nil {
		return fmt.Errorf("watch is nil")
	}
	if err := watch.Validate(); err != nil {
		return err
	}
	// 检查渠道存在
	if _, err := ChannelGet(watch.ChannelID, ctx); err != nil {
		return fmt.Errorf("channel not found: %w", err)
	}
	return db.GetDB().WithContext(ctx).Create(watch).Error
}

func ChannelModelWatchUpdate(ctx context.Context, req *model.ChannelModelWatchUpdateRequest) (*model.ChannelModelWatch, error) {
	if req == nil {
		return nil, fmt.Errorf("update request is nil")
	}
	if req.ID <= 0 {
		return nil, fmt.Errorf("invalid id")
	}
	var watch model.ChannelModelWatch
	conn := db.GetDB().WithContext(ctx)
	if err := conn.First(&watch, req.ID).Error; err != nil {
		return nil, err
	}

	if req.ChannelID != nil {
		if *req.ChannelID <= 0 {
			return nil, fmt.Errorf("invalid channel_id")
		}
		// 确认渠道存在
		if _, err := ChannelGet(*req.ChannelID, ctx); err != nil {
			return nil, fmt.Errorf("channel not found: %w", err)
		}
		watch.ChannelID = *req.ChannelID
	}
	if req.ModelName != nil {
		watch.ModelName = normalizeModelName(*req.ModelName)
	}
	if req.WebhookURL != nil {
		watch.WebhookURL = strings.TrimSpace(*req.WebhookURL)
	}
	if req.Secret != nil {
		watch.Secret = strings.TrimSpace(*req.Secret)
	}
	if req.DedupMinutes != nil {
		if *req.DedupMinutes <= 0 {
			watch.DedupMinutes = defaultWatchDedupMinutes
		} else {
			watch.DedupMinutes = *req.DedupMinutes
		}
	}
	if req.Enabled != nil {
		watch.Enabled = *req.Enabled
	}
	if err := watch.Validate(); err != nil {
		return nil, err
	}
	if err := conn.Save(&watch).Error; err != nil {
		return nil, err
	}
	return &watch, nil
}

func ChannelModelWatchDelete(ctx context.Context, id int) error {
	if id <= 0 {
		return fmt.Errorf("invalid id")
	}
	return db.GetDB().WithContext(ctx).Delete(&model.ChannelModelWatch{}, id).Error
}

// ChannelModelWatchMatch 返回命中的监听配置（按照模型新增匹配），并应用去重窗口。
func ChannelModelWatchMatch(ctx context.Context, channelID int, models []string, now time.Time) ([]ChannelModelWatchHit, error) {
	if channelID <= 0 {
		return nil, fmt.Errorf("invalid channel_id")
	}
	if len(models) == 0 {
		return nil, nil
	}
	modelSet := make(map[string]struct{}, len(models))
	for _, m := range models {
		if n := normalizeModelName(m); n != "" {
			modelSet[n] = struct{}{}
		}
	}
	if len(modelSet) == 0 {
		return nil, nil
	}
	var watches []model.ChannelModelWatch
	if err := db.GetDB().WithContext(ctx).
		Model(&model.ChannelModelWatch{}).
		Where("channel_id = ? AND enabled = ?", channelID, true).
		Find(&watches).Error; err != nil {
		return nil, err
	}
	hits := make([]ChannelModelWatchHit, 0, len(watches))
	for _, w := range watches {
		target := normalizeModelName(w.ModelName)
		if _, ok := modelSet[target]; !ok {
			continue
		}
		// 去重窗口
		dedupMinutes := w.DedupMinutes
		if dedupMinutes <= 0 {
			dedupMinutes = defaultWatchDedupMinutes
		}
		if w.LastNotifiedAt != nil {
			if now.Sub(*w.LastNotifiedAt) < time.Duration(dedupMinutes)*time.Minute {
				continue
			}
		}
		hits = append(hits, ChannelModelWatchHit{
			Watch:        w,
			MatchedModel: target,
		})
	}
	return hits, nil
}

func ChannelModelWatchTouchNotified(ctx context.Context, ids []int, t time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return db.GetDB().WithContext(ctx).
		Model(&model.ChannelModelWatch{}).
		Where("id IN ?", ids).
		Updates(map[string]interface{}{
			"last_notified_at": t,
			"updated_at":       t,
		}).Error
}
