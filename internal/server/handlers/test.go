package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Handle(testChannel),
		)
}

type TestChannelRequest struct {
	ChannelID int    `json:"channel_id" binding:"required"`
	Model     string `json:"model" binding:"required"`
}

type TestChannelResponse struct {
	Success bool   `json:"success"`
	Latency int64  `json:"latency"`
	Error   string `json:"error,omitempty"`
}

func testChannel(c *gin.Context) {
	var req TestChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	channel, err := op.ChannelGet(req.ChannelID, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "channel not found")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	start := time.Now()
	testErr := doTestChannel(ctx, channel, req.Model)
	latency := time.Since(start).Milliseconds()

	result := TestChannelResponse{
		Success: testErr == nil,
		Latency: latency,
	}
	if testErr != nil {
		result.Error = testErr.Error()
	}

	resp.Success(c, result)
}

func doTestChannel(ctx context.Context, channel *model.Channel, modelName string) error {
	httpClient, err := helper.ChannelHttpClient(channel)
	if err != nil {
		return fmt.Errorf("failed to create http client: %w", err)
	}

	baseURL := channel.GetBaseUrl()
	if baseURL == "" {
		return fmt.Errorf("no base url configured")
	}

	key, err := channel.GetChannelKey()
	if err != nil {
		return fmt.Errorf("failed to get channel key: %w", err)
	}
	if key.ChannelKey == "" {
		return fmt.Errorf("no api key configured")
	}

	var reqBody []byte
	var url string
	var headers map[string]string

	switch channel.Type {
	case outbound.OutboundTypeAnthropic:
		url = baseURL + "/messages"
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":      modelName,
			"max_tokens": 1,
			"messages": []map[string]string{
				{"role": "user", "content": "Hi"},
			},
		})
		headers = map[string]string{
			"Content-Type":      "application/json",
			"X-Api-Key":         key.ChannelKey,
			"Anthropic-Version": "2023-06-01",
		}

	case outbound.OutboundTypeGemini:
		url = baseURL + "/models/" + modelName + ":generateContent"
		reqBody, _ = json.Marshal(map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "Hi"},
					},
				},
			},
			"generationConfig": map[string]interface{}{
				"maxOutputTokens": 1,
			},
		})
		headers = map[string]string{
			"Content-Type":   "application/json",
			"X-Goog-Api-Key": key.ChannelKey,
		}

	default:
		url = baseURL + "/chat/completions"
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":      modelName,
			"max_tokens": 1,
			"messages": []map[string]string{
				{"role": "user", "content": "Hi"},
			},
		})
		headers = map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + key.ChannelKey,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	for _, h := range channel.CustomHeader {
		req.Header.Set(h.HeaderKey, h.HeaderValue)
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("upstream error %d: %s", response.StatusCode, string(body))
	}

	return nil
}
