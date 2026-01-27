package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listChannel),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateChannel),
		).
		AddRoute(
			router.NewRoute("/enable", http.MethodPost).
				Handle(enableChannel),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteChannel),
		).
		AddRoute(
			router.NewRoute("/fetch-model", http.MethodPost).
				Handle(fetchModel),
		).
		AddRoute(
			router.NewRoute("/keys/import", http.MethodPost).
				Handle(importChannelKeys),
		).
		AddRoute(
			router.NewRoute("/keys/validate", http.MethodPost).
				Handle(ValidateKeysHandler),
		).
		AddRoute(
			router.NewRoute("/keys/validate/start", http.MethodPost).
				Handle(ValidateKeysStartHandler),
		).
		AddRoute(
			router.NewRoute("/keys/validate/status", http.MethodGet).
				Handle(ValidateKeysStatusHandler),
		).
		AddRoute(
			router.NewRoute("/keys/validate/cancel", http.MethodPost).
				Handle(ValidateKeysCancelHandler),
		).
		AddRoute(
			router.NewRoute("/keys/list", http.MethodPost).
				Handle(listChannelKeys),
		).
		AddRoute(
			router.NewRoute("/keys/restore-invalid", http.MethodPost).
				Handle(restoreInvalidChannelKeys),
		).
		AddRoute(
			router.NewRoute("/keys/clear-invalid", http.MethodPost).
				Handle(clearInvalidChannelKeys),
		)
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/sync", http.MethodPost).
				Handle(syncChannel),
		).
		AddRoute(
			router.NewRoute("/sync/:id", http.MethodPost).
				Handle(syncSingleChannel),
		).
		AddRoute(
			router.NewRoute("/last-sync-time", http.MethodGet).
				Handle(getLastSyncTime),
		)
}

func listChannel(c *gin.Context) {
	channels, err := op.ChannelList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	for i, channel := range channels {
		stats := op.StatsChannelGet(channel.ID)
		channels[i].Stats = &stats
		// 避免密钥池渠道携带海量 keys 导致前端卡顿；前端有独立密钥池管理页。
		if channel.KeyPoolEnabled {
			channels[i].Keys = []model.ChannelKey{}
		}
	}
	resp.Success(c, channels)
}

func createChannel(c *gin.Context) {
	var channel model.Channel
	if err := c.ShouldBindJSON(&channel); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelCreate(&channel, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	stats := op.StatsChannelGet(channel.ID)
	channel.Stats = &stats
	if channel.KeyPoolEnabled {
		channel.Keys = []model.ChannelKey{}
	}
	go func(channel *model.Channel) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		modelStr := channel.Model + "," + channel.CustomModel
		modelArray := strings.Split(modelStr, ",")
		helper.LLMPriceAddToDB(modelArray, ctx)
		helper.ChannelBaseUrlDelayUpdate(channel, ctx)
		helper.ChannelAutoGroup(channel, ctx)
	}(&channel)
	resp.Success(c, channel)
}

func updateChannel(c *gin.Context) {
	var req model.ChannelUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channel, err := op.ChannelUpdate(&req, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	stats := op.StatsChannelGet(channel.ID)
	channel.Stats = &stats
	if channel.KeyPoolEnabled {
		channel.Keys = []model.ChannelKey{}
	}
	go func(channel *model.Channel) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		modelStr := channel.Model + "," + channel.CustomModel
		modelArray := strings.Split(modelStr, ",")
		helper.LLMPriceAddToDB(modelArray, ctx)
		helper.ChannelBaseUrlDelayUpdate(channel, ctx)
		helper.ChannelAutoGroup(channel, ctx)
	}(channel)
	resp.Success(c, channel)
}

func enableChannel(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func deleteChannel(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.ChannelDel(idNum, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

type importKeysRequest struct {
	ChannelID int    `json:"channel_id" binding:"required"`
	Text      string `json:"text" binding:"required"`
}

type listKeysRequest struct {
	ChannelID int   `json:"channel_id" binding:"required"`
	Page      int   `json:"page"`
	PageSize  int   `json:"page_size"`
	Enabled   *bool `json:"enabled"`
}

func importChannelKeys(c *gin.Context) {
	var req importKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	added, skipped, err := op.ChannelKeysImport(c.Request.Context(), req.ChannelID, req.Text)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{
		"added":   added,
		"skipped": skipped,
	})
}

func listChannelKeys(c *gin.Context) {
	var req listKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	keys, total, err := op.ChannelKeysPage(c.Request.Context(), req.ChannelID, req.Page, req.PageSize, req.Enabled)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{
		"items":     keys,
		"total":     total,
		"page":      req.Page,
		"page_size": req.PageSize,
	})
}

type channelIDRequest struct {
	ChannelID int `json:"channel_id" binding:"required"`
}

func restoreInvalidChannelKeys(c *gin.Context) {
	var req channelIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	count, err := op.ChannelKeysRestoreInvalid(c.Request.Context(), req.ChannelID)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"restored": count})
}

func clearInvalidChannelKeys(c *gin.Context) {
	var req channelIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	count, err := op.ChannelKeysClearInvalid(c.Request.Context(), req.ChannelID)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, gin.H{"cleared": count})
}
func fetchModel(c *gin.Context) {
	var request model.Channel
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	models, err := helper.FetchModels(c.Request.Context(), request)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, models)
}

func syncChannel(c *gin.Context) {
	task.SyncModelsTask()
	resp.Success(c, nil)
}

func getLastSyncTime(c *gin.Context) {
	time := task.GetLastSyncModelsTime()
	resp.Success(c, time)
}

// syncSingleChannel 同步单个渠道的模型列表
func syncSingleChannel(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "无效的渠道ID")
		return
	}

	// 获取渠道信息
	channel, err := op.ChannelGet(id, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "渠道不存在")
		return
	}

	// 获取模型列表
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	if !channel.Enabled {
		resp.Error(c, http.StatusBadRequest, "渠道已禁用，无法同步模型")
		return
	}

	fetchModels, err := helper.FetchModels(ctx, *channel)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// 更新渠道模型
	newModels := strings.Join(fetchModels, ",")
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{
		ID:    channel.ID,
		Model: &newModels,
	}, ctx); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// 自动分组
	if len(fetchModels) > 0 {
		helper.ChannelAutoGroup(channel, ctx)
	}

	resp.Success(c, nil)
}
