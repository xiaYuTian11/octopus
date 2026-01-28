package handlers

import (
	"net/http"
	"strconv"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/channel/watch").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listChannelModelWatch),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(createChannelModelWatch),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(updateChannelModelWatch),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteChannelModelWatch),
		).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(testChannelModelWatch),
		)
}

func listChannelModelWatch(c *gin.Context) {
	var channelIDPtr *int
	if idStr := c.Query("channel_id"); idStr != "" {
		if id, err := strconv.Atoi(idStr); err == nil && id > 0 {
			channelIDPtr = &id
		} else {
			resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
			return
		}
	}
	items, err := op.ChannelModelWatchList(c.Request.Context(), channelIDPtr)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, items)
}

func createChannelModelWatch(c *gin.Context) {
	var req model.ChannelModelWatch
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelModelWatchCreate(c.Request.Context(), &req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, req)
}

func updateChannelModelWatch(c *gin.Context) {
	var req model.ChannelModelWatchUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	item, err := op.ChannelModelWatchUpdate(c.Request.Context(), &req)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, item)
}

func deleteChannelModelWatch(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.ChannelModelWatchDelete(c.Request.Context(), id); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func testChannelModelWatch(c *gin.Context) {
	var req struct {
		ID int `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := helper.TestChannelModelWatch(c.Request.Context(), req.ID); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, gin.H{"ok": true})
}
