package helper

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

var webhookHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
}

type channelModelWatchPayload struct {
	Event         string   `json:"event"`
	Trigger       string   `json:"trigger"`
	ChannelID     int      `json:"channel_id"`
	ChannelName   string   `json:"channel_name"`
	MatchedModels []string `json:"matched_models"`
	AllModels     []string `json:"all_models"`
	Timestamp     string   `json:"timestamp"`
}

// NotifyChannelModelWatch 根据新增模型命中监听配置并发送 webhook。
// candidateModels 应该是本次新增的模型列表；allModels 用于告警 payload。
func NotifyChannelModelWatch(ctx context.Context, channel model.Channel, candidateModels []string, allModels []string, trigger string) {
	now := time.Now()
	hits, err := op.ChannelModelWatchMatch(ctx, channel.ID, candidateModels, now)
	if err != nil {
		log.Warnf("channel %d watch match failed: %v", channel.ID, err)
		return
	}
	if len(hits) == 0 {
		return
	}

	payloadAll := make([]string, 0, len(allModels))
	for _, m := range allModels {
		if m == "" {
			continue
		}
		payloadAll = append(payloadAll, m)
	}

	for _, hit := range hits {
		fireChannelModelWebhook(ctx, channel, hit, payloadAll, trigger)
	}

	ids := make([]int, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.Watch.ID)
	}
	if err := op.ChannelModelWatchTouchNotified(ctx, ids, now); err != nil {
		log.Warnf("channel %d watch touch failed: %v", channel.ID, err)
	}
}

func fireChannelModelWebhook(ctx context.Context, channel model.Channel, hit op.ChannelModelWatchHit, allModels []string, trigger string) {
	payload := channelModelWatchPayload{
		Event:         "channel_model_detected",
		Trigger:       trigger,
		ChannelID:     channel.ID,
		ChannelName:   channel.Name,
		MatchedModels: []string{hit.MatchedModel},
		AllModels:     allModels,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Warnf("marshal webhook payload failed: %v", err)
		return
	}
	// 重试 3 次指数退避
	url := hit.Watch.WebhookURL
	secret := hit.Watch.Secret
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			sig := signPayload(body, secret)
			req.Header.Set("X-Octopus-Signature", sig)
		}
		resp, err := webhookHTTPClient.Do(req)
		cancel()
		if err != nil {
			lastErr = err
		} else {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		}
		time.Sleep(time.Duration(1<<attempt) * 200 * time.Millisecond)
	}
	log.Warnf("webhook notify failed (channel=%d, watch=%d): %v", channel.ID, hit.Watch.ID, lastErr)
}

func signPayload(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
