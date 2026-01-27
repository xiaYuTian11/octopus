package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ChannelModelWatch 用于监听渠道新增模型并发送 webhook 的配置。
type ChannelModelWatch struct {
	ID             int        `json:"id" gorm:"primaryKey"`
	ChannelID      int        `json:"channel_id" gorm:"index:idx_channel_model_webhook,unique;not null"`
	ModelName      string     `json:"model_name" gorm:"size:255;not null;index:idx_channel_model_webhook,unique"`
	WebhookURL     string     `json:"webhook_url" gorm:"size:1024;not null;index:idx_channel_model_webhook,unique"`
	Secret         string     `json:"secret" gorm:"size:255"`
	DedupMinutes   int        `json:"dedup_minutes" gorm:"default:60"`
	Enabled        bool       `json:"enabled" gorm:"default:true"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ChannelModelWatchUpdateRequest 更新请求结构。
type ChannelModelWatchUpdateRequest struct {
	ID           int     `json:"id" binding:"required"`
	ChannelID    *int    `json:"channel_id,omitempty"`
	ModelName    *string `json:"model_name,omitempty"`
	WebhookURL   *string `json:"webhook_url,omitempty"`
	Secret       *string `json:"secret,omitempty"`
	DedupMinutes *int    `json:"dedup_minutes,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
}

func (w *ChannelModelWatch) normalize() {
	w.ModelName = strings.ToLower(strings.TrimSpace(w.ModelName))
	w.WebhookURL = strings.TrimSpace(w.WebhookURL)
	w.Secret = strings.TrimSpace(w.Secret)
	if w.DedupMinutes <= 0 {
		w.DedupMinutes = 60
	}
}

func (w *ChannelModelWatch) Validate() error {
	w.normalize()
	if w.ChannelID <= 0 {
		return fmt.Errorf("invalid channel_id")
	}
	if w.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if w.WebhookURL == "" {
		return fmt.Errorf("webhook_url is required")
	}
	if _, err := url.ParseRequestURI(w.WebhookURL); err != nil {
		return fmt.Errorf("webhook_url is invalid: %w", err)
	}
	return nil
}
