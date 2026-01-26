package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	transformerModel "github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

const (
	precheckGroupTag = "clarify-precheck"
	planPrefix       = "[[plan]]"
)

var precheckTimeoutSec = 10

func init() {
	if raw := strings.TrimSpace(os.Getenv(strings.ToUpper(conf.APP_NAME) + "_PRECHECK_TIMEOUT")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			precheckTimeoutSec = v
		}
	}
}

type precheckResult struct {
	CompletedRequest string   `json:"completed_request"`
	MissingSlots     []string `json:"missing_slots"`
	AskUser          bool     `json:"ask_user"`
}

// maybeHandlePrecheck runs the optional pre-analysis flow.
// Return handled=true if response is already written to client.
func maybeHandlePrecheck(c *gin.Context, internalRequest *transformerModel.InternalLLMRequest, inAdapter transformerModel.Inbound) (bool, error) {
	if internalRequest == nil || !internalRequest.IsChatRequest() {
		return false, nil
	}

	userIdx, userMsg := firstPrecheckMessage(internalRequest.Messages)
	if userMsg == nil {
		return false, nil
	}

	userText, ok := messageContentText(userMsg.Content)
	if !ok {
		return false, nil
	}

	cleanText, hasPrefix := stripPlanPrefix(userText)
	if !hasPrefix {
		return false, nil
	}

	// Strip the prefix in-place so downstream won't see it even if precheck is skipped.
	internalRequest.Messages[userIdx].Content = messageContentFromText(cleanText)
	originalText := strings.TrimSpace(cleanText)

	precheckGroup, err := op.GroupGetByTag(precheckGroupTag, c.Request.Context())
	if err != nil {
		log.Infof("precheck tag %s not configured, skip precheck", precheckGroupTag)
		return false, nil
	}

	result, err := runPrecheck(c, internalRequest, &precheckGroup, originalText)
	if err != nil {
		log.Warnf("precheck failed: %v, fallback to direct relay", err)
		return false, nil
	}
	if result == nil {
		return false, nil
	}

	// When ask_user=true, return clarify message and stop.
	if result.AskUser && len(result.MissingSlots) > 0 {
		if err := sendClarifyResponse(c, inAdapter, internalRequest, originalText, result); err != nil {
			return false, err
		}
		return true, nil
	}

	merged := buildMergedContent(originalText, result.CompletedRequest, result.MissingSlots)
	internalRequest.Messages[userIdx].Content = messageContentFromText(merged)
	return false, nil
}

func runPrecheck(c *gin.Context, baseReq *transformerModel.InternalLLMRequest, precheckGroup *dbmodel.Group, originalText string) (*precheckResult, error) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(precheckTimeoutSec)*time.Second)
	defer cancel()

	precheckReq := cloneInternalRequest(baseReq)
	stream := false
	precheckReq.Stream = &stream
	precheckReq.Model = precheckGroup.Name
	prompt := buildPrecheckPrompt()
	precheckReq.Messages = append([]transformerModel.Message{
		{
			Role:    "system",
			Content: messageContentFromText(prompt),
		},
	}, precheckReq.Messages...)

	b := balancer.GetBalancer(precheckGroup.Mode)
	itemCount := len(precheckGroup.Items)
	maxAttempts := itemCount
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	attempted := make(map[int]bool, itemCount)

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("precheck context done: %w", ctx.Err())
		default:
		}

		item := b.Select(precheckGroup.Items)
		if item == nil {
			return nil, fmt.Errorf("precheck: no available channel")
		}
		if attempted[item.ChannelID] {
			if len(attempted) >= itemCount {
				break
			}
			continue
		}
		attempted[item.ChannelID] = true

		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			lastErr = err
			continue
		}
		if !channel.Enabled {
			lastErr = fmt.Errorf("channel %s disabled", channel.Name)
			continue
		}

		outAdapter := outbound.Get(channel.Type)
		if outAdapter == nil {
			lastErr = fmt.Errorf("unsupported channel type %d", channel.Type)
			continue
		}
		if precheckReq.IsEmbeddingRequest() || !precheckReq.IsChatRequest() {
			return nil, fmt.Errorf("precheck request must be chat completion")
		}
		if !outbound.IsChatChannelType(channel.Type) {
			lastErr = fmt.Errorf("channel %s not compatible with chat request", channel.Name)
			continue
		}

		precheckReq.Model = item.ModelName
		usedKey, err := op.ChannelSelectKey(ctx, channel)
		if err != nil || usedKey.ChannelKey == "" {
			lastErr = fmt.Errorf("precheck select key failed: %w", err)
			continue
		}

		outReq, err := outAdapter.TransformRequest(ctx, precheckReq, channel.GetBaseUrl(), usedKey.ChannelKey)
		if err != nil {
			lastErr = err
			continue
		}

		copyPrecheckHeaders(c.Request, outReq, channel.CustomHeader)

		httpClient, err := helper.ChannelHttpClient(channel)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := httpClient.Do(outReq)
		if err != nil {
			updatePrecheckKey(ctx, channel, usedKey, 0, err)
			lastErr = err
			continue
		}
		var parsedResult *precheckResult
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				bodyStr := string(body)
				updatePrecheckKey(ctx, channel, usedKey, resp.StatusCode, fmt.Errorf("%s", bodyStr))
				lastErr = fmt.Errorf("precheck upstream error %d: %s", resp.StatusCode, bodyStr)
				return
			}

			internalResp, err := outAdapter.TransformResponse(ctx, resp)
			if err != nil {
				updatePrecheckKey(ctx, channel, usedKey, resp.StatusCode, err)
				lastErr = err
				return
			}

			result, err := parsePrecheckResult(internalResp, originalText)
			if err != nil {
				updatePrecheckKey(ctx, channel, usedKey, resp.StatusCode, err)
				lastErr = err
				return
			}
			updatePrecheckKey(ctx, channel, usedKey, resp.StatusCode, nil)
			lastErr = nil
			result.CompletedRequest = strings.TrimSpace(result.CompletedRequest)
			if result.CompletedRequest == "" {
				result.CompletedRequest = originalText
			}
			parsedResult = result
		}()

		if parsedResult != nil {
			return parsedResult, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("precheck failed without explicit error")
}

func parsePrecheckResult(resp *transformerModel.InternalLLMResponse, original string) (*precheckResult, error) {
	text := extractResponseText(resp)
	if text == "" {
		return nil, fmt.Errorf("precheck empty response")
	}
	text = strings.TrimSpace(text)

	var result precheckResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		// Try to locate JSON body inside the text.
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(text[start:end+1]), &result); err2 != nil {
				// fallback: treat whole text as completion
				result.CompletedRequest = text
			}
		} else {
			result.CompletedRequest = text
		}
	}

	if result.CompletedRequest == "" {
		result.CompletedRequest = original
	}
	return &result, nil
}

func stripPlanPrefix(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, planPrefix) {
		return text, false
	}
	without := strings.TrimSpace(trimmed[len(planPrefix):])
	return without, true
}

func firstPrecheckMessage(msgs []transformerModel.Message) (int, *transformerModel.Message) {
	for i := range msgs {
		if strings.ToLower(strings.TrimSpace(msgs[i].Role)) == "user" {
			if _, ok := messageContentText(msgs[i].Content); ok {
				return i, &msgs[i]
			}
		}
	}
	for i := range msgs {
		if _, ok := messageContentText(msgs[i].Content); ok {
			return i, &msgs[i]
		}
	}
	return -1, nil
}

func messageContentText(content transformerModel.MessageContent) (string, bool) {
	if content.Content != nil {
		return *content.Content, true
	}
	for _, part := range content.MultipleContent {
		// treat empty type as text as well.
		if part.Text != nil && (part.Type == "" || part.Type == "text") {
			return *part.Text, true
		}
	}
	return "", false
}

func messageContentFromText(text string) transformerModel.MessageContent {
	return transformerModel.MessageContent{Content: &text}
}

func buildPrecheckPrompt() string {
	return "你是前置分析助手。请返回 JSON，包含: completed_request（补全后的清晰描述），missing_slots（需补充要点数组，如系统类型、目标用户、核心功能、非功能约束、交付形态），ask_user（布尔值，若需要用户补充信息则为 true）。若信息已足够，missing_slots 为空，ask_user 为 false。仅输出 JSON。"
}

func buildMergedContent(original, completion string, missing []string) string {
	var sb strings.Builder
	sb.WriteString("[原始请求]\n")
	sb.WriteString(strings.TrimSpace(original))
	if completion != "" {
		sb.WriteString("\n\n[补全]\n")
		sb.WriteString(strings.TrimSpace(completion))
	}
	if len(missing) > 0 {
		sb.WriteString("\n\n[待补充]\n")
		for _, slot := range missing {
			slot = strings.TrimSpace(slot)
			if slot == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(slot)
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

func cloneInternalRequest(req *transformerModel.InternalLLMRequest) *transformerModel.InternalLLMRequest {
	cp := *req
	if len(req.Messages) > 0 {
		cp.Messages = append([]transformerModel.Message(nil), req.Messages...)
	}
	return &cp
}

func copyPrecheckHeaders(src *http.Request, dst *http.Request, custom []dbmodel.CustomHeader) {
	for key, values := range src.Header {
		if hopByHopHeaders[strings.ToLower(key)] {
			continue
		}
		for _, value := range values {
			dst.Header.Set(key, value)
		}
	}
	if len(custom) > 0 {
		for _, header := range custom {
			dst.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
}

func extractResponseText(resp *transformerModel.InternalLLMResponse) string {
	if resp == nil {
		return ""
	}
	for _, choice := range resp.Choices {
		if choice.Message != nil {
			if txt, ok := messageContentText(choice.Message.Content); ok {
				return txt
			}
		}
		if choice.Delta != nil {
			if txt, ok := messageContentText(choice.Delta.Content); ok {
				return txt
			}
		}
	}
	return ""
}

func updatePrecheckKey(ctx context.Context, channel *dbmodel.Channel, key dbmodel.ChannelKey, statusCode int, err error) {
	key.StatusCode = statusCode
	key.LastUseTimeStamp = time.Now().Unix()
	if channel.KeyPoolEnabled {
		threshold := channel.KeyFailThreshold
		if threshold <= 0 {
			threshold = 3
		}
		if shouldCountKeyFailure(statusCode, err) {
			key.FailureCount++
			if key.FailureCount >= threshold {
				key.Enabled = false
				key.DisabledReason = failureReason(err, statusCode)
			}
		}
		op.ChannelKeyUpdateImmediate(ctx, key)
	} else {
		op.ChannelKeyUpdate(key)
	}
}

func sendClarifyResponse(c *gin.Context, inAdapter transformerModel.Inbound, internalRequest *transformerModel.InternalLLMRequest, original string, result *precheckResult) error {
	var sb strings.Builder
	sb.WriteString("需要补充以下信息后再发送：\n")
	for _, slot := range result.MissingSlots {
		slot = strings.TrimSpace(slot)
		if slot == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(slot)
		sb.WriteString("\n")
	}
	sb.WriteString("\n原始请求：\n")
	sb.WriteString(strings.TrimSpace(original))
	if result.CompletedRequest != "" {
		sb.WriteString("\n\n补全建议：\n")
		sb.WriteString(strings.TrimSpace(result.CompletedRequest))
	}
	message := sb.String()

	payload := &transformerModel.InternalLLMResponse{
		ID:      fmt.Sprintf("precheck-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   internalRequest.Model,
		Choices: []transformerModel.Choice{
			{
				Message: &transformerModel.Message{
					Role:    "assistant",
					Content: messageContentFromText(message),
				},
			},
		},
	}

	out, err := inAdapter.TransformResponse(c.Request.Context(), payload)
	if err != nil {
		return err
	}
	c.Data(http.StatusOK, "application/json", out)
	return nil
}
