package handlers

import (
	"context"
	"fmt"
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

type validateJobState string

const (
	validateJobStatePending  validateJobState = "pending"
	validateJobStateRunning  validateJobState = "running"
	validateJobStateSuccess  validateJobState = "success"
	validateJobStateError    validateJobState = "error"
	validateJobStateCanceled validateJobState = "canceled"
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

type validateJob struct {
	ID          string             `json:"id"`
	ChannelID   int                `json:"channel_id"`
	Model       string             `json:"model"`
	Timeout     time.Duration      `json:"timeout"`
	Concurrency int                `json:"concurrency"`
	Total       int64              `json:"total"`
	Tested      int64              `json:"tested"`
	Disabled    int64              `json:"disabled"`
	Success     int64              `json:"success"`
	State       validateJobState   `json:"state"`
	Error       string             `json:"error,omitempty"`
	StartedAt   time.Time          `json:"started_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	cancel      context.CancelFunc `json:"-"`
	mu          sync.Mutex         `json:"-"`
}

func (j *validateJob) touch() {
	j.mu.Lock()
	j.UpdatedAt = time.Now()
	j.mu.Unlock()
}

func (j *validateJob) setState(state validateJobState, errMsg string) {
	j.mu.Lock()
	j.State = state
	j.Error = errMsg
	j.UpdatedAt = time.Now()
	j.mu.Unlock()
}

var (
	validateJobs   = make(map[string]*validateJob)
	validateJobsMu sync.RWMutex
)

func deleteValidateJob(id string) {
	validateJobsMu.Lock()
	delete(validateJobs, id)
	validateJobsMu.Unlock()
}

func scheduleValidateJobCleanup(id string, delay time.Duration) {
	time.AfterFunc(delay, func() {
		deleteValidateJob(id)
	})
}

func storeValidateJob(job *validateJob) {
	validateJobsMu.Lock()
	validateJobs[job.ID] = job
	validateJobsMu.Unlock()
}

func loadValidateJob(id string) (*validateJob, bool) {
	validateJobsMu.RLock()
	job, ok := validateJobs[id]
	validateJobsMu.RUnlock()
	return job, ok
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
	pageSize := 500 // 更小分页，降低内存峰值
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

// ValidateKeysStartHandler 启动异步校验任务，返回任务 ID。
func ValidateKeysStartHandler(c *gin.Context) {
	var req validateKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
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

	total, err := op.ChannelKeysCount(c.Request.Context(), req.ChannelID, nil)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	jobID := fmt.Sprintf("vk-%d-%d", req.ChannelID, time.Now().UnixNano())
	job := &validateJob{
		ID:          jobID,
		ChannelID:   req.ChannelID,
		Model:       req.Model,
		Timeout:     timeout,
		Concurrency: concurrency,
		Total:       total,
		State:       validateJobStatePending,
		StartedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	storeValidateJob(job)

	jobCtx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel

	go func() {
		job.setState(validateJobStateRunning, "")
		if err := runValidateJob(jobCtx, job, ch); err != nil {
			if job.State == validateJobStateCanceled {
				scheduleValidateJobCleanup(job.ID, 5*time.Minute)
				return
			}
			job.setState(validateJobStateError, err.Error())
			scheduleValidateJobCleanup(job.ID, 5*time.Minute)
			return
		}
		job.setState(validateJobStateSuccess, "")
		scheduleValidateJobCleanup(job.ID, 5*time.Minute)
	}()

	resp.Success(c, gin.H{
		"job_id": jobID,
		"total":  total,
	})
}

// ValidateKeysStatusHandler 查询校验任务进度。
func ValidateKeysStatusHandler(c *gin.Context) {
	jobID := c.Query("id")
	if jobID == "" {
		resp.Error(c, http.StatusBadRequest, "missing job id")
		return
	}
	job, ok := loadValidateJob(jobID)
	if !ok {
		resp.Error(c, http.StatusNotFound, "job not found")
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	resp.Success(c, gin.H{
		"id":          job.ID,
		"channel_id":  job.ChannelID,
		"model":       job.Model,
		"state":       job.State,
		"error":       job.Error,
		"total":       job.Total,
		"tested":      atomic.LoadInt64(&job.Tested),
		"disabled":    atomic.LoadInt64(&job.Disabled),
		"success":     atomic.LoadInt64(&job.Success),
		"started_at":  job.StartedAt.Unix(),
		"updated_at":  job.UpdatedAt.Unix(),
		"timeout_sec": job.Timeout.Seconds(),
	})
}

// ValidateKeysCancelHandler 取消正在进行的校验任务。
func ValidateKeysCancelHandler(c *gin.Context) {
	var req struct {
		ID string `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	job, ok := loadValidateJob(req.ID)
	if !ok {
		resp.Error(c, http.StatusNotFound, "job not found")
		return
	}
	job.setState(validateJobStateCanceled, "")
	if job.cancel != nil {
		job.cancel()
	}
	scheduleValidateJobCleanup(job.ID, 5*time.Minute)
	resp.Success(c, gin.H{"canceled": true})
}

// runValidateJob 执行校验任务并更新 job 进度。
func runValidateJob(ctx context.Context, job *validateJob, ch *model.Channel) error {
	concurrency := job.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}

	jobs := make(chan model.ChannelKey, concurrency*2)
	var wg sync.WaitGroup

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

			atomic.AddInt64(&job.Tested, 1)

			if len(k.ChannelKey) < 8 {
				k.Enabled = false
				k.DisabledReason = "too short key"
				_ = op.ChannelKeyUpdateImmediate(ctx, k)
				atomic.AddInt64(&job.Disabled, 1)
				job.touch()
				continue
			}

			keyCtx, cancel := context.WithTimeout(ctx, job.Timeout)
			testErr := validateSingleKey(keyCtx, ch, k, job.Model)
			cancel()

			k.LastUseTimeStamp = time.Now().Unix()
			if testErr != nil {
				k.Enabled = false
				k.DisabledReason = testErr.Error()
				_ = op.ChannelKeyUpdateImmediate(ctx, k)
				atomic.AddInt64(&job.Disabled, 1)
			} else {
				k.Enabled = true
				k.FailureCount = 0
				k.StatusCode = 200
				k.DisabledReason = ""
				_ = op.ChannelKeyUpdateImmediate(ctx, k)
				atomic.AddInt64(&job.Success, 1)
			}
			job.touch()
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}

	page := 1
	pageSize := 500 // 更小分页，降低内存峰值
	for {
		keys, _, err := op.ChannelKeysPage(ctx, job.ChannelID, page, pageSize, nil)
		if err != nil {
			close(jobs)
			wg.Wait()
			return err
		}
		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return context.Canceled
			case jobs <- key:
			}
		}
		page++
	}

	close(jobs)
	wg.Wait()
	return nil
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
