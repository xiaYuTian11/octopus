package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
)

type validateKeysRequest struct {
	ChannelID int     `json:"channel_id" binding:"required"`
	Model     string  `json:"model" binding:"required"`  // 用于请求的目标模型
	Timeout   float64 `json:"timeout,omitempty"`         // 单 Key 超时（秒），默认 10
}

type validateKeysResult struct {
	Tested   int `json:"tested"`
	Disabled int `json:"disabled"`
	Success  int `json:"success"`
}

// ValidateKeysHandler 按模型逐个测试密钥池的 Key，失败的自动禁用。
func ValidateKeysHandler(c *gin.Context) {
	var req validateKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if req.ChannelID <= 0 {
		resp.Error(c, http.StatusBadRequest, "invalid channel_id")
		return
	}
	if req.Model == "" {
		resp.Error(c, http.StatusBadRequest, "model is required")
		return
	}

	timeout := time.Duration(req.Timeout * float64(time.Second))
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	ch, err := op.ChannelGet(req.ChannelID, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "channel not found")
		return
	}
	if !ch.KeyPoolEnabled {
		resp.Error(c, http.StatusBadRequest, "channel is not key-pool enabled")
		return
	}

	keys, total, err := op.ChannelKeysPage(c.Request.Context(), req.ChannelID, 1, 5000, nil)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if total == 0 || len(keys) == 0 {
		resp.Success(c, validateKeysResult{Tested: 0, Disabled: 0, Success: 0})
		return
	}

	tested, disabled, success := 0, 0, 0
	for _, k := range keys {
		if !k.Enabled {
			// 已经禁用的跳过，避免无效调用
			continue
		}
		tested++

		// 跳过明显占位 key
		if len(k.ChannelKey) < 8 {
			k.Enabled = false
			k.DisabledReason = "too short key"
			_ = op.ChannelKeyUpdateImmediate(c.Request.Context(), k)
			disabled++
			continue
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		testErr := validateSingleKey(ctx, ch, k, req.Model)
		cancel()

		if testErr != nil {
			k.Enabled = false
			k.DisabledReason = testErr.Error()
			_ = op.ChannelKeyUpdateImmediate(c.Request.Context(), k)
			disabled++
		} else {
			// 标记成功，重置失败次数
			k.FailureCount = 0
			k.StatusCode = 200
			k.DisabledReason = ""
			_ = op.ChannelKeyUpdateImmediate(c.Request.Context(), k)
			success++
		}
	}

	resp.Success(c, validateKeysResult{
		Tested:   tested,
		Disabled: disabled,
		Success:  success,
	})
}

// validateSingleKey 使用指定模型对单个 Key 做一次模型列表请求，验证其可用性。
func validateSingleKey(ctx context.Context, ch *model.Channel, key model.ChannelKey, modelName string) error {
	chCopy := *ch
	chCopy.Enabled = true
	key.Enabled = true
	key.StatusCode = 0
	key.LastUseTimeStamp = 0
	chCopy.Keys = []model.ChannelKey{key}
	chCopy.Model = modelName
	chCopy.CustomModel = ""
	chCopy.MatchRegex = nil
	// 关闭密钥池模式，强制使用传入的单个 Key 进行测试
	chCopy.KeyPoolEnabled = false
	_, err := helper.FetchModels(ctx, chCopy)
	return err
}
