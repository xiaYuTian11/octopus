package model

import (
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

type AutoGroupType int

const (
	AutoGroupTypeNone  AutoGroupType = 0 //不自动分组
	AutoGroupTypeFuzzy AutoGroupType = 1 //模糊匹配
	AutoGroupTypeExact AutoGroupType = 2 //准确匹配
	AutoGroupTypeRegex AutoGroupType = 3 //正则匹配
)

type Channel struct {
	ID               int                   `json:"id" gorm:"primaryKey"`
	Name             string                `json:"name" gorm:"unique;not null"`
	Type             outbound.OutboundType `json:"type"`
	Enabled          bool                  `json:"enabled" gorm:"default:true"`
	BaseUrls         []BaseUrl             `json:"base_urls" gorm:"serializer:json"`
	Keys             []ChannelKey          `json:"keys" gorm:"foreignKey:ChannelID"`
	Model            string                `json:"model"`
	CustomModel      string                `json:"custom_model"`
	Proxy            bool                  `json:"proxy" gorm:"default:false"`
	AutoSync         bool                  `json:"auto_sync" gorm:"default:false"`
	AutoGroup        AutoGroupType         `json:"auto_group" gorm:"default:0"`
	KeyPoolEnabled   bool                  `json:"key_pool_enabled" gorm:"default:false"`
	KeyFailThreshold int                   `json:"key_fail_threshold" gorm:"default:3"`
	KeyCount         int                   `json:"key_count" gorm:"-"`
	KeyEnabledCount  int                   `json:"key_enabled_count" gorm:"-"`
	KeyDisabledCount int                   `json:"key_disabled_count" gorm:"-"`
	CustomHeader     []CustomHeader        `json:"custom_header" gorm:"serializer:json"`
	ParamOverride    *string               `json:"param_override"`
	ChannelProxy     *string               `json:"channel_proxy"`
	MatchRegex       *string               `json:"match_regex"`
	Stats            *StatsChannel         `json:"stats,omitempty" gorm:"foreignKey:ChannelID"`
	// keyMutex 保护密钥选择过程，防止并发竞态条件
	keyMutex sync.Mutex `json:"-" gorm:"-"`
}

type BaseUrl struct {
	URL   string `json:"url"`
	Delay int    `json:"delay"`
}

type CustomHeader struct {
	HeaderKey   string `json:"header_key"`
	HeaderValue string `json:"header_value"`
}

type ChannelKey struct {
	ID               int     `json:"id" gorm:"primaryKey"`
	ChannelID        int     `json:"channel_id"`
	Enabled          bool    `json:"enabled" gorm:"default:true"`
	ChannelKey       string  `json:"channel_key"`
	StatusCode       int     `json:"status_code"`
	LastUseTimeStamp int64   `json:"last_use_time_stamp"`
	TotalCost        float64 `json:"total_cost"`
	Remark           string  `json:"remark"`
	FailureCount     int     `json:"failure_count" gorm:"default:0"`
	DisabledReason   string  `json:"disabled_reason"`
	RateLimitRPM     int     `json:"rate_limit_rpm" gorm:"default:0"` // 每分钟最大请求数，0 表示不限制
}

// ChannelUpdateRequest 渠道更新请求 - 仅包含变更的数据
type ChannelUpdateRequest struct {
	ID               int                    `json:"id" binding:"required"`
	Name             *string                `json:"name,omitempty"`
	Type             *outbound.OutboundType `json:"type,omitempty"`
	Enabled          *bool                  `json:"enabled,omitempty"`
	BaseUrls         *[]BaseUrl             `json:"base_urls,omitempty"`
	Model            *string                `json:"model,omitempty"`
	CustomModel      *string                `json:"custom_model,omitempty"`
	Proxy            *bool                  `json:"proxy,omitempty"`
	AutoSync         *bool                  `json:"auto_sync,omitempty"`
	AutoGroup        *AutoGroupType         `json:"auto_group,omitempty"`
	KeyPoolEnabled   *bool                  `json:"key_pool_enabled,omitempty"`
	KeyFailThreshold *int                   `json:"key_fail_threshold,omitempty"`
	CustomHeader     *[]CustomHeader        `json:"custom_header,omitempty"`
	ChannelProxy     *string                `json:"channel_proxy,omitempty"`
	ParamOverride    *string                `json:"param_override,omitempty"`

	KeysToAdd    []ChannelKeyAddRequest    `json:"keys_to_add,omitempty"`
	KeysToUpdate []ChannelKeyUpdateRequest `json:"keys_to_update,omitempty"`
	KeysToDelete []int                     `json:"keys_to_delete,omitempty"`
}

type ChannelKeyAddRequest struct {
	Enabled      bool   `json:"enabled"`
	ChannelKey   string `json:"channel_key" binding:"required"`
	Remark       string `json:"remark"`
	RateLimitRPM int    `json:"rate_limit_rpm"` // 每分钟最大请求数，0 表示不限制
}

type ChannelKeyUpdateRequest struct {
	ID           int     `json:"id" binding:"required"`
	Enabled      *bool   `json:"enabled,omitempty"`
	ChannelKey   *string `json:"channel_key,omitempty"`
	Remark       *string `json:"remark,omitempty"`
	RateLimitRPM *int    `json:"rate_limit_rpm,omitempty"` // 每分钟最大请求数，0 表示不限制
}

// ChannelFetchModelRequest is used by /channel/fetch-model (not persisted).
type ChannelFetchModelRequest struct {
	Type    outbound.OutboundType `json:"type" binding:"required"`
	BaseURL string                `json:"base_url" binding:"required"`
	Key     string                `json:"key" binding:"required"`
	Proxy   bool                  `json:"proxy"`
}

func (c *Channel) GetBaseUrl() string {
	if c == nil || len(c.BaseUrls) == 0 {
		return ""
	}

	bestURL := ""
	bestDelay := 0
	bestSet := false

	for _, bu := range c.BaseUrls {
		if bu.URL == "" {
			continue
		}
		if !bestSet || bu.Delay < bestDelay {
			bestURL = bu.URL
			bestDelay = bu.Delay
			bestSet = true
		}
	}

	return bestURL
}

// GetChannelKey 选择最佳的渠道密钥
// 使用互斥锁保护，防止并发调用时的竞态条件
func (c *Channel) GetChannelKey() ChannelKey {
	if c == nil || len(c.Keys) == 0 {
		return ChannelKey{}
	}

	// 加锁保护密钥选择过程，防止多个 goroutine 同时选中同一个 key
	c.keyMutex.Lock()
	defer c.keyMutex.Unlock()

	nowSec := time.Now().Unix()

	best := ChannelKey{}
	bestCost := 0.0
	bestSet := false

	for _, k := range c.Keys {
		if !k.Enabled || k.ChannelKey == "" {
			continue
		}
		if k.StatusCode == 429 && k.LastUseTimeStamp > 0 {
			if nowSec-k.LastUseTimeStamp < int64(5*time.Minute/time.Second) {
				continue
			}
		}
		if !bestSet || k.TotalCost < bestCost {
			best = k
			bestCost = k.TotalCost
			bestSet = true
		}
	}

	if !bestSet {
		return ChannelKey{}
	}
	return best
}

// GetAvailableKeys 返回所有可用的密钥列表，按 TotalCost 排序
// 用于速率限制检查时遍历所有可用 key
func (c *Channel) GetAvailableKeys() []ChannelKey {
	if c == nil || len(c.Keys) == 0 {
		return nil
	}

	c.keyMutex.Lock()
	defer c.keyMutex.Unlock()

	nowSec := time.Now().Unix()
	available := make([]ChannelKey, 0, len(c.Keys))

	for _, k := range c.Keys {
		if !k.Enabled || k.ChannelKey == "" {
			continue
		}
		if k.StatusCode == 429 && k.LastUseTimeStamp > 0 {
			if nowSec-k.LastUseTimeStamp < int64(5*time.Minute/time.Second) {
				continue
			}
		}
		available = append(available, k)
	}

	// 按 TotalCost 排序（升序）
	for i := 0; i < len(available)-1; i++ {
		for j := i + 1; j < len(available); j++ {
			if available[j].TotalCost < available[i].TotalCost {
				available[i], available[j] = available[j], available[i]
			}
		}
	}

	return available
}
