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
	planPrefix       = "/plan"
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
	// Ready indicates the request is clear enough to proceed.
	// When true, ClarifiedRequest contains the enhanced request.
	// When false, ClarifyMessage contains the follow-up question for user.
	Ready            bool   `json:"ready"`
	ClarifiedRequest string `json:"clarified_request"`
	ClarifyMessage   string `json:"-"` // Natural language follow-up question

	// Legacy fields for backward compatibility
	CompletedRequest string   `json:"completed_request"`
	MissingSlots     []string `json:"missing_slots"`
	AskUser          bool     `json:"ask_user"`
}

// maybeHandlePrecheck runs the optional pre-analysis flow.
// Return handled=true if response is already written to client.
func maybeHandlePrecheck(c *gin.Context, internalRequest *transformerModel.InternalLLMRequest, inAdapter transformerModel.Inbound) (bool, error) {
	if internalRequest == nil || !internalRequest.IsChatRequest() {
		log.Debugf("precheck: skip - not a chat request")
		return false, nil
	}

	userIdx, userMsg := firstPrecheckMessage(internalRequest.Messages)
	if userMsg == nil {
		log.Debugf("precheck: skip - no user message found")
		return false, nil
	}

	userText, ok := messageContentText(userMsg.Content)
	if !ok {
		log.Debugf("precheck: skip - cannot extract text from user message")
		return false, nil
	}

	log.Debugf("precheck: checking user message (idx=%d), text length=%d, starts with: %.50s...", userIdx, len(userText), userText)

	cleanText, hasPrefix := stripPlanPrefix(userText)
	if !hasPrefix {
		log.Debugf("precheck: skip - no %s prefix found", planPrefix)
		return false, nil
	}

	log.Infof("precheck: %s prefix detected, starting pre-analysis", planPrefix)

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

	// If precheck returned a clarify message (natural language follow-up), return it to user.
	if result.ClarifyMessage != "" {
		if err := sendDirectClarifyResponse(c, inAdapter, internalRequest, result.ClarifyMessage); err != nil {
			return false, err
		}
		return true, nil
	}

	// Legacy: When ask_user=true, return clarify message and stop.
	if result.AskUser && len(result.MissingSlots) > 0 {
		if err := sendClarifyResponse(c, inAdapter, internalRequest, originalText, result); err != nil {
			return false, err
		}
		return true, nil
	}

	// Use clarified request if available, otherwise use completed_request (legacy)
	clarified := result.ClarifiedRequest
	if clarified == "" {
		clarified = result.CompletedRequest
	}
	if clarified == "" {
		clarified = originalText
	}

	// If clarified request is the same as original, just use original (no need to merge)
	if strings.TrimSpace(clarified) == strings.TrimSpace(originalText) {
		internalRequest.Messages[userIdx].Content = messageContentFromText(originalText)
		return false, nil
	}

	// Build merged content with original and clarified request
	merged := buildMergedContent(originalText, clarified, result.MissingSlots)
	internalRequest.Messages[userIdx].Content = messageContentFromText(merged)
	log.Infof("precheck: request clarified, merged content length=%d", len(merged))
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

	// Try to parse as JSON first
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		// Try to locate JSON body inside the text
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start >= 0 && end > start {
			jsonStr := text[start : end+1]
			if err2 := json.Unmarshal([]byte(jsonStr), &result); err2 != nil {
				// Not valid JSON - treat as natural language clarify message
				result.ClarifyMessage = text
				return &result, nil
			}
			// Valid JSON found, check if there's text before it (could be clarify message)
			if start > 0 {
				prefix := strings.TrimSpace(text[:start])
				if len(prefix) > 50 { // Significant text before JSON, likely a clarify message
					result.ClarifyMessage = text
					return &result, nil
				}
			}
		} else {
			// No JSON found - this is a natural language clarify message
			result.ClarifyMessage = text
			return &result, nil
		}
	}

	// If JSON parsed successfully, check the ready field
	if result.Ready {
		// Request is clear, use clarified_request
		if result.ClarifiedRequest == "" {
			result.ClarifiedRequest = original
		}
	} else if result.ClarifiedRequest == "" && result.CompletedRequest == "" && !result.AskUser {
		// JSON parsed but no useful content - treat original text as clarify message
		// This handles cases where model outputs JSON-like structure but it's actually a question
		result.ClarifyMessage = text
	}

	// Legacy compatibility: if completed_request is set but clarified_request is not
	if result.ClarifiedRequest == "" && result.CompletedRequest != "" {
		result.ClarifiedRequest = result.CompletedRequest
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
	// Find the LAST user message (most recent), not the first one.
	// This is important for multi-turn conversations where [[plan]] prefix
	// is in the latest user message, not the first one.
	lastUserIdx := -1
	for i := range msgs {
		if strings.ToLower(strings.TrimSpace(msgs[i].Role)) == "user" {
			if _, ok := messageContentText(msgs[i].Content); ok {
				lastUserIdx = i
			}
		}
	}
	if lastUserIdx >= 0 {
		return lastUserIdx, &msgs[lastUserIdx]
	}
	// Fallback: find the last message with text content
	for i := len(msgs) - 1; i >= 0; i-- {
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
	return `你是需求澄清助手。用户的请求可能不够清晰或缺少关键信息。

请分析用户的请求，判断是否需要追问以获取更多信息。

**输出格式要求**：
- 如果需要追问用户，直接输出友好的追问内容（使用 Markdown 格式，可以用列表、标题等让回复更清晰）
- 如果信息已经足够清晰，输出 JSON: {"ready": true, "clarified_request": "补全后的清晰描述"}

**判断标准**：
- 项目类型不明确（如"2pai"可能指树莓派、某个框架等）
- 缺少核心功能描述
- 缺少技术栈偏好
- 缺少目标用户或使用场景
- 存在歧义需要确认

**追问时的要求**：
- 友好、专业的语气
- 列出可能的选项帮助用户选择
- 提供具体的示例引导用户
- 使用 emoji 让回复更生动`
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
	// Clear parameters that are incompatible with precheck requests.
	// Precheck always uses stream=false, so stream_options must be cleared.
	cp.StreamOptions = nil
	// Clear tool-related parameters as precheck doesn't need function calling.
	cp.Tools = nil
	cp.ToolChoice = nil
	cp.ParallelToolCalls = nil
	// Clear response format constraints as precheck expects JSON output.
	cp.ResponseFormat = nil
	// Clear reasoning parameters that may not be supported by precheck model.
	cp.ReasoningEffort = ""
	cp.ReasoningBudget = nil
	cp.EnableThinking = nil
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

// sendDirectClarifyResponse sends the precheck model's natural language response directly to the user.
// This is used when the precheck model generates a follow-up question in natural language format.
func sendDirectClarifyResponse(c *gin.Context, inAdapter transformerModel.Inbound, internalRequest *transformerModel.InternalLLMRequest, clarifyMessage string) error {
	finishReason := "stop"
	payload := &transformerModel.InternalLLMResponse{
		ID:      fmt.Sprintf("precheck-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   internalRequest.Model,
		Choices: []transformerModel.Choice{
			{
				Index: 0,
				Message: &transformerModel.Message{
					Role:    "assistant",
					Content: messageContentFromText(clarifyMessage),
				},
				FinishReason: &finishReason,
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
