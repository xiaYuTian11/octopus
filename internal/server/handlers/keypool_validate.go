package handlers

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

type validateKeysRequest struct {
	ChannelID   int     `json:"channel_id" binding:"required"`
	Model       string  `json:"model" binding:"required"` // 用于请求的目标模型
	Timeout     float64 `json:"timeout,omitempty"`        // 单 Key 超时（秒），默认 10
	Concurrency int     `json:"concurrency,omitempty"`    // 并发数，默认 5，最大 20
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
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	if concurrency > 20 {
		concurrency = 20
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

	var tested, disabled, success int64
	ctx := c.Request.Context()
	jobs := make(chan model.ChannelKey, concurrency*2)

	worker := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("validate key panic: %v", r)
			}
		}()
		for k := range jobs {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if !k.Enabled {
				continue
			}

			atomic.AddInt64(&tested, 1)

			// 跳过明显无效的短 key
			if len(k.ChannelKey) < 8 {
				k.Enabled = false
				k.DisabledReason = "too short key"
				_ = op.ChannelKeyUpdateImmediate(ctx, k)
				atomic.AddInt64(&disabled, 1)
				continue
			}

			keyCtx, cancel := context.WithTimeout(ctx, timeout)
			testErr := validateSingleKey(keyCtx, ch, k, req.Model)
			cancel()

			k.LastUseTimeStamp = time.Now().Unix()
			if testErr != nil {
				k.Enabled = false
				k.DisabledReason = testErr.Error()
				_ = op.ChannelKeyUpdateImmediate(ctx, k)
				atomic.AddInt64(&disabled, 1)
			} else {
				// 成功则重置失败计数并确保启用
				k.Enabled = true
				k.FailureCount = 0
				k.StatusCode = 200
				k.DisabledReason = ""
				_ = op.ChannelKeyUpdateImmediate(ctx, k)
				atomic.AddInt64(&success, 1)
			}
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}

	page := 1
	pageSize := 5000
	for {
		keys, _, err := op.ChannelKeysPage(ctx, req.ChannelID, page, pageSize, nil)
		if err != nil {
			close(jobs)
			wg.Wait()
			resp.Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				resp.Error(c, http.StatusRequestTimeout, "request canceled")
				return
			case jobs <- key:
			}
		}

		page++
	}

	close(jobs)
	wg.Wait()

	resp.Success(c, validateKeysResult{
		Tested:   int(tested),
		Disabled: int(disabled),
		Success:  int(success),
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
