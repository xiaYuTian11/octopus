package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/transformer/inbound"
	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/tmaxmax/go-sse"
)

// Handler 处理入站请求并转发到上游服务
func Handler(inboundType inbound.InboundType, c *gin.Context) {
	// 解析请求
	internalRequest, inAdapter, err := parseRequest(inboundType, c)
	if err != nil {
		return
	}
	supportedModels := c.GetString("supported_models")
	if supportedModels != "" {
		supportedModelsArray := strings.Split(supportedModels, ",")
		if !slices.Contains(supportedModelsArray, internalRequest.Model) {
			resp.Error(c, http.StatusBadRequest, "model not supported")
			return
		}
	}

	// Optional precheck (explicit trigger only).
	// TODO: 前置校验功能暂时禁用，如需恢复请取消下方注释
	// if handled, err := maybeHandlePrecheck(c, internalRequest, inAdapter); err != nil {
	// 	resp.Error(c, http.StatusInternalServerError, err.Error())
	// 	return
	// } else if handled {
	// 	return
	// }

	// 初始化统计和日志
	apiKeyID := c.GetInt("api_key_id")
	metrics := NewRelayMetrics(internalRequest.Model)
	metrics.SetInternalRequest(internalRequest)
	metrics.SetAPIKeyID(apiKeyID)
	// 获取通道分组
	group, err := op.GroupGetMap(internalRequest.Model, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "model not found")
		return
	}

	// 使用 map 记录已尝试的渠道，避免重复尝试
	var lastErr error
	itemCount := len(group.Items)
	attemptedChannels := make(map[int]bool, itemCount)
	b := balancer.GetBalancer(group.Mode)

	// 最多尝试渠道数的2倍(允许负载均衡器有一定的随机性)
	// 但一旦所有渠道都尝试过就立即退出
	maxAttempts := itemCount * 2
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-c.Request.Context().Done():
			log.Infof("request context canceled, stopping retry")
			return
		default:
		}

		item := b.Select(group.Items)
		if item == nil {
			resp.Error(c, http.StatusServiceUnavailable, "no available channel")
			return
		}

		// 检查是否已尝试过此渠道
		if attemptedChannels[item.ChannelID] {
			// 如果所有渠道都已尝试过，退出循环
			if len(attemptedChannels) >= itemCount {
				log.Infof("All %d channels have been attempted, stopping retry", itemCount)
				break
			}
			// 否则继续尝试获取下一个渠道
			continue
		}

		// 标记此渠道已尝试
		attemptedChannels[item.ChannelID] = true

		channel, err := op.ChannelGet(item.ChannelID, c.Request.Context())
		if err != nil {
			log.Warnf("failed to get channel %d: %v (attempt %d/%d, tried %d/%d channels)",
				item.ChannelID, err, attempt+1, maxAttempts, len(attemptedChannels), itemCount)
			lastErr = err
			continue
		}
		if channel.Enabled == false {
			log.Warnf("channel %s is disabled (attempt %d/%d, tried %d/%d channels)",
				channel.Name, attempt+1, maxAttempts, len(attemptedChannels), itemCount)
			lastErr = fmt.Errorf("channel %s is disabled", channel.Name)
			continue
		}

		internalRequest.Model = item.ModelName
		metrics.SetChannel(channel.ID, channel.Name, item.ModelName)

		usedKey, err := op.ChannelSelectKey(c.Request.Context(), channel)
		if err != nil {
			// 检查是否是速率限制错误
			if errors.Is(err, op.ErrAllKeysRateLimited) || errors.Is(err, op.ErrRateLimitExceeded) {
				log.Warnf("channel %s rate limited: %v", channel.Name, err)
			} else {
				log.Warnf("no available key for channel %s: %v", channel.Name, err)
			}
			lastErr = err
			continue
		}
		if usedKey.ChannelKey == "" {
			log.Warnf("empty key for channel %s", channel.Name)
			lastErr = fmt.Errorf("no available key")
			continue
		}

		keyTail := ""
		if len(usedKey.ChannelKey) > 4 {
			keyTail = usedKey.ChannelKey[len(usedKey.ChannelKey)-4:]
		} else {
			keyTail = usedKey.ChannelKey
		}

		log.Infof("request model %s, mode: %d, forwarding to channel: %s model: %s (attempt %d/%d, tried %d/%d channels), key_id=%d, key_tail=%s",
			internalRequest.Model, group.Mode, channel.Name, item.ModelName, attempt+1, maxAttempts, len(attemptedChannels), itemCount, usedKey.ID, keyTail)

		outAdapter := outbound.Get(channel.Type)
		if outAdapter == nil {
			log.Warnf("unsupported channel type: %d for channel: %s (attempt %d/%d, tried %d/%d channels)",
				channel.Type, channel.Name, attempt+1, maxAttempts, len(attemptedChannels), itemCount)
			lastErr = fmt.Errorf("unsupported channel type: %d", channel.Type)
			continue
		}

		// 验证 channel 类型与请求类型匹配
		if internalRequest.IsEmbeddingRequest() && !outbound.IsEmbeddingChannelType(channel.Type) {
			log.Warnf("channel type %d is not compatible with embedding request for channel: %s", channel.Type, channel.Name)
			lastErr = fmt.Errorf("channel type %d not compatible with embedding request", channel.Type)
			continue
		}

		if internalRequest.IsChatRequest() && !outbound.IsChatChannelType(channel.Type) {
			log.Warnf("channel type %d is not compatible with chat request for channel: %s", channel.Type, channel.Name)
			lastErr = fmt.Errorf("channel type %d not compatible with chat request", channel.Type)
			continue
		}

		rc := &relayContext{
			c:                    c,
			inAdapter:            inAdapter,
			outAdapter:           outAdapter,
			internalRequest:      internalRequest,
			channel:              channel,
			metrics:              metrics,
			usedKey:              usedKey,
			keyTail:              keyTail,
			firstTokenTimeOutSec: group.FirstTokenTimeOut,
			validator:            NewResponseValidator(),
		}

		if statusCode, err := rc.forward(); err == nil {
			rc.collectResponse()
			rc.usedKey.StatusCode = statusCode
			rc.usedKey.LastUseTimeStamp = time.Now().Unix()
			rc.usedKey.TotalCost += metrics.Stats.InputCost + metrics.Stats.OutputCost
			if channel.KeyPoolEnabled {
				rc.usedKey.FailureCount = 0
				rc.usedKey.DisabledReason = ""
				op.ChannelKeyUpdateImmediate(c.Request.Context(), rc.usedKey)
			} else {
				op.ChannelKeyUpdate(rc.usedKey)
			}
			metrics.Save(c.Request.Context(), true, nil)
			return
		} else {
			rc.usedKey.StatusCode = statusCode
			rc.usedKey.LastUseTimeStamp = time.Now().Unix()
			if channel.KeyPoolEnabled {
				threshold := channel.KeyFailThreshold
				if threshold <= 0 {
					threshold = 3
				}
				if shouldCountKeyFailure(statusCode, err) {
					rc.usedKey.FailureCount++
					if rc.usedKey.FailureCount >= threshold {
						rc.usedKey.Enabled = false
						rc.usedKey.DisabledReason = failureReason(err, statusCode)
					}
				}
				op.ChannelKeyUpdateImmediate(c.Request.Context(), rc.usedKey)
			} else {
				op.ChannelKeyUpdate(rc.usedKey)
			}
			if c.Writer.Written() {
				// Streaming responses may have already started; retrying would corrupt the client stream.
				rc.collectResponse()
				metrics.Save(c.Request.Context(), false, err)
				return
			}
			requestURL := rc.metrics.RequestURL
			requestMethod := rc.metrics.RequestMethod
			if requestURL != "" {
				log.Warnf("channel %s failed on %s %s: %v (attempt %d/%d, tried %d/%d channels)",
					channel.Name, requestMethod, requestURL, err, attempt+1, maxAttempts, len(attemptedChannels), itemCount)
				lastErr = fmt.Errorf("channel %s failed on %s %s: %v", channel.Name, requestMethod, requestURL, err)
			} else {
				log.Warnf("channel %s failed: %v (attempt %d/%d, tried %d/%d channels)",
					channel.Name, err, attempt+1, maxAttempts, len(attemptedChannels), itemCount)
				lastErr = fmt.Errorf("channel %s failed: %v", channel.Name, err)
			}
		}
	}

	// 所有通道都失败
	metrics.Save(c.Request.Context(), false, lastErr)

	// 根据错误类型提供更明确的错误消息
	errorMsg := "all channels failed"
	statusCode := http.StatusBadGateway

	if lastErr != nil {
		if errors.Is(lastErr, op.ErrAllKeysRateLimited) || errors.Is(lastErr, op.ErrRateLimitExceeded) {
			errorMsg = "all channels have reached rate limit, please try again later"
			statusCode = http.StatusTooManyRequests
			log.Warnf("All channels rate limited: %v", lastErr)
		} else if IsResponseQualityError(lastErr) {
			errorMsg = "all channels returned empty or invalid responses"
			log.Warnf("All channels failed with quality issues: %v", lastErr)
		}
	}

	resp.Error(c, statusCode, errorMsg)
}

func shouldCountKeyFailure(statusCode int, err error) bool {
	if statusCode >= 500 || statusCode == 401 || statusCode == 403 || statusCode == 429 {
		return true
	}
	if statusCode == 0 && err != nil {
		return true
	}
	return err != nil
}

func failureReason(err error, statusCode int) string {
	if err != nil {
		msg := err.Error()
		if len(msg) > 256 {
			return msg[:256]
		}
		return msg
	}
	if statusCode > 0 {
		return fmt.Sprintf("status %d", statusCode)
	}
	return ""
}

// parseRequest 解析并验证入站请求
func parseRequest(inboundType inbound.InboundType, c *gin.Context) (*model.InternalLLMRequest, model.Inbound, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return nil, nil, err
	}

	// DEBUG: 记录客户端发送的所有 header，用于排查问题
	log.Warnf("[DEBUG-HEADERS] Client headers: %v", c.Request.Header)

	inAdapter := inbound.Get(inboundType)
	internalRequest, err := inAdapter.TransformRequest(c.Request.Context(), body)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return nil, nil, err
	}

	// Pass through the original query parameters
	internalRequest.Query = c.Request.URL.Query()

	if err := internalRequest.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return nil, nil, err
	}

	return internalRequest, inAdapter, nil
}

// forward 转发请求到上游服务
func (rc *relayContext) forward() (int, error) {
	ctx := rc.c.Request.Context()

	// 构建出站请求
	// 如果客户端发送了 Authorization header，使用客户端的 key；否则使用渠道配置的 key
	clientAuth := rc.c.Request.Header.Get("Authorization")
	usedKeyForRequest := rc.usedKey.ChannelKey
	if clientAuth != "" {
		// 客户端发送了 Authorization header，使用客户端的 key（透传模式）
		// 提取 Bearer 后面的 key
		if strings.HasPrefix(clientAuth, "Bearer ") {
			clientKey := strings.TrimPrefix(clientAuth, "Bearer ")
			if strings.HasPrefix(clientKey, "sk-octopus-") {
				// 这是 octopus 系统的 key，需要透传给上游
				// 但先检查是否应该使用客户端的 key
				log.Warnf("[DEBUG-KEY] Using client key for passthrough mode")
			}
		}
	}

	outboundRequest, err := rc.outAdapter.TransformRequest(
		ctx,
		rc.internalRequest,
		rc.channel.GetBaseUrl(),
		usedKeyForRequest,
	)
	if err != nil {
		log.Warnf("failed to create request: %v", err)
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	// 记录完整的请求URL和方法到metrics（必须在发送请求前记录，以便在失败时也能保存）
	if outboundRequest.URL != nil {
		rc.metrics.SetRequestInfo(outboundRequest.URL.String(), outboundRequest.Method)
	}

	// 复制请求头（包括 Authorization）
	rc.copyHeaders(outboundRequest)

	// 发送请求
	response, err := rc.sendRequest(outboundRequest)
	if err != nil {
		return 0, fmt.Errorf("failed to send request %s %s: %w", outboundRequest.Method, outboundRequest.URL.String(), err)
	}
	defer response.Body.Close()

	// 检查响应状态
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return 0, fmt.Errorf("failed to read response body for %s %s: %w", outboundRequest.Method, outboundRequest.URL.String(), err)
		}
		rc.logUpstreamError(outboundRequest, response, body)
		return 0, fmt.Errorf("upstream error %s %s: %d: %s", outboundRequest.Method, outboundRequest.URL.String(), response.StatusCode, string(body))
	}

	// 处理响应
	if rc.internalRequest.Stream != nil && *rc.internalRequest.Stream {
		if err := rc.handleStreamResponse(ctx, response); err != nil {
			return 0, err
		}
		return response.StatusCode, nil
	}
	if err := rc.handleResponse(ctx, response); err != nil {
		return 0, err
	}
	return response.StatusCode, nil
}

// copyHeaders 复制请求头，完全透传客户端的 header
// 不过滤任何 header，让程序像一个真正的透明代理
// 这样上游服务收到的请求与 Cherry Studio 直连时完全一致
func (rc *relayContext) copyHeaders(outboundRequest *http.Request) {
	// 记录原始的 Authorization header（如果有）
	originalAuth := outboundRequest.Header.Get("Authorization")

	// 完全复制客户端的所有 header
	for key, values := range rc.c.Request.Header {
		outboundRequest.Header[key] = values
	}

	// 记录 header 复制的详细信息
	finalAuth := outboundRequest.Header.Get("Authorization")
	hasXApiKey := outboundRequest.Header.Get("x-api-key") != ""

	log.Infof("[HEADER-COPY] Channel: %s, Original Auth: %s, Final Auth: %s, Has x-api-key: %v",
		rc.channel.Name,
		maskAuthHeader(originalAuth),
		maskAuthHeader(finalAuth),
		hasXApiKey)

	// DEBUG: 记录转发的完整 header 和代理信息
	log.Warnf("[DEBUG-OUTGOING] Channel %s proxy=%v full_headers: %v",
		rc.channel.Name, rc.channel.Proxy, maskSensitiveHeaders(outboundRequest.Header))
}

// maskAuthHeader 脱敏认证 header
func maskAuthHeader(auth string) string {
	if auth == "" {
		return "<empty>"
	}
	if len(auth) > 20 {
		return auth[:10] + "..." + auth[len(auth)-4:]
	}
	return auth[:min(len(auth), 10)] + "..."
}

// maskSensitiveHeaders 脱敏敏感 header
func maskSensitiveHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	for k, v := range headers {
		key := strings.ToLower(k)
		if strings.Contains(key, "auth") || strings.Contains(key, "key") || strings.Contains(key, "token") {
			result[k] = "<redacted>"
		} else {
			result[k] = strings.Join(v, ", ")
		}
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sendRequest 发送 HTTP 请求
func (rc *relayContext) sendRequest(req *http.Request) (*http.Response, error) {
	httpClient, err := helper.ChannelHttpClient(rc.channel)
	if err != nil {
		log.Warnf("failed to get http client: %v", err)
		return nil, err
	}

	response, err := httpClient.Do(req)
	if err != nil {
		requestURL := ""
		if req != nil && req.URL != nil {
			requestURL = req.URL.String()
		}
		log.Warnf("failed to send request %s %s: %v", req.Method, requestURL, err)
		return nil, err
	}

	return response, nil
}

// logUpstreamError 记录更详细的上游错误信息，便于排查例如 CDN/Cloudflare 502 等问题
func (rc *relayContext) logUpstreamError(req *http.Request, resp *http.Response, body []byte) {
	const maxBodyLog = 2048 // 避免日志过大

	requestURL := ""
	method := ""
	if req != nil && req.URL != nil {
		requestURL = req.URL.String()
		method = req.Method
	}

	// 记录请求头（脱敏）
	reqHeaders := make(map[string][]string, len(req.Header))
	for k, v := range req.Header {
		if strings.EqualFold(k, "Authorization") || strings.Contains(strings.ToLower(k), "api-key") {
			reqHeaders[k] = []string{"<redacted>"}
			continue
		}
		reqHeaders[k] = v
	}

	reqContentLength := req.ContentLength

	// 摘取关键响应头，方便定位链路
	headerSnapshot := map[string]string{
		"CF-RAY":         resp.Header.Get("CF-RAY"),
		"Server":         resp.Header.Get("Server"),
		"Via":            resp.Header.Get("Via"),
		"Content-Type":   resp.Header.Get("Content-Type"),
		"Content-Length": resp.Header.Get("Content-Length"),
		"Location":       resp.Header.Get("Location"),
	}

	bodyStr := string(body)
	if len(bodyStr) > maxBodyLog {
		bodyStr = bodyStr[:maxBodyLog] + "...(truncated)"
	}

	log.Warnf("upstream non-2xx | channel=%s | method=%s | url=%s | status=%d | key_id=%d | key_tail=%s | resp_headers=%v | req_headers=%v | req_content_length=%d | body=%s",
		rc.channel.Name, method, requestURL, resp.StatusCode, rc.usedKey.ID, rc.keyTail, headerSnapshot, reqHeaders, reqContentLength, bodyStr)
}

// handleStreamResponse 处理流式响应
func (rc *relayContext) handleStreamResponse(ctx context.Context, response *http.Response) error {
	// 流式响应应当是 SSE
	// 某些上游可能会返回非SSE的JSON响应 (由于 Accept headers 配置错误)
	if ct := response.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16*1024))
		return fmt.Errorf("upstream returned non-SSE content-type %q for stream request: %s", ct, string(body))
	}

	// 设置 SSE 响应头
	rc.c.Header("Content-Type", "text/event-stream")
	rc.c.Header("Cache-Control", "no-cache")
	rc.c.Header("Connection", "keep-alive")
	rc.c.Header("X-Accel-Buffering", "no")

	firstToken := true

	// Streaming "time to first token" timeout: only applies before we write anything to the client.
	// We read SSE events in a goroutine so we can race the first meaningful output against a timer.
	type sseReadResult struct {
		data string
		err  error
	}
	results := make(chan sseReadResult, 1)
	go func() {
		defer close(results)
		readCfg := &sse.ReadConfig{MaxEventSize: maxSSEEventSize}
		for ev, err := range sse.Read(response.Body, readCfg) {
			if err != nil {
				results <- sseReadResult{err: err}
				return
			}
			results <- sseReadResult{data: ev.Data}
		}
	}()

	var firstTokenTimer *time.Timer
	var firstTokenC <-chan time.Time
	if firstToken && rc.firstTokenTimeOutSec > 0 {
		firstTokenTimer = time.NewTimer(time.Duration(rc.firstTokenTimeOutSec) * time.Second)
		firstTokenC = firstTokenTimer.C
		defer func() {
			if firstTokenTimer != nil {
				firstTokenTimer.Stop()
			}
		}()
	}

	for {
		// 检查客户端是否断开
		select {
		case <-ctx.Done():
			log.Infof("client disconnected, stopping stream")
			return nil
		case <-firstTokenC:
			// Abort upstream stream before any client writes; caller will retry next channel.
			log.Warnf("first token timeout (%ds), switching channel", rc.firstTokenTimeOutSec)
			_ = response.Body.Close()
			return fmt.Errorf("first token timeout (%ds)", rc.firstTokenTimeOutSec)
		case r, ok := <-results:
			if !ok {
				log.Infof("stream end")
				return nil
			}
			if r.err != nil {
				log.Warnf("failed to read event: %v", r.err)
				return fmt.Errorf("failed to read stream event: %w", r.err)
			}

			// 转换流式数据
			data, err := rc.transformStreamData(ctx, r.data)
			if err != nil {
				// 如果是响应质量错误且还未写入客户端,可以重试其他渠道
				if IsResponseQualityError(err) && !rc.c.Writer.Written() {
					log.Warnf("stream validation failed before client write, will retry next channel: %v", err)
					_ = response.Body.Close()
					return err
				}
				// 其他错误或已写入客户端,继续处理
				if err != nil {
					log.Warnf("stream transform error: %v", err)
					continue
				}
			}
			if len(data) == 0 {
				continue
			}
			// 记录首个 Token 时间
			if firstToken {
				rc.metrics.SetFirstTokenTime(time.Now())
				firstToken = false
				// Disable the first-token timer once we have meaningful output.
				if firstTokenTimer != nil {
					if !firstTokenTimer.Stop() {
						select {
						case <-firstTokenTimer.C:
						default:
						}
					}
					firstTokenTimer = nil
					firstTokenC = nil
				}
			}

			rc.c.Writer.Write(data)
			rc.c.Writer.Flush()
		}
	}
}

// transformStreamData 转换流式数据
func (rc *relayContext) transformStreamData(ctx context.Context, data string) ([]byte, error) {
	// 上游格式 → 内部格式
	internalStream, err := rc.outAdapter.TransformStream(ctx, []byte(data))
	if err != nil {
		log.Warnf("failed to transform stream: %v", err)
		return nil, err
	}
	if internalStream == nil {
		return nil, nil
	}

	log.Infof("[DEBUG-TOKEN] transformStreamData: calling inAdapter(%p).TransformStream, has usage: %v",
		rc.inAdapter, internalStream.Usage != nil)
	if internalStream.Usage != nil {
		log.Infof("[DEBUG-TOKEN] transformStreamData: usage in stream - prompt=%d, completion=%d",
			internalStream.Usage.PromptTokens, internalStream.Usage.CompletionTokens)
	}

	// 验证流式响应块的质量
	if rc.validator != nil {
		if err := rc.validator.ValidateStreamChunk(internalStream); err != nil {
			log.Warnf("stream chunk validation failed: %v", err)
			// 对于流式响应,如果检测到质量问题,返回错误以触发重试
			if IsResponseQualityError(err) {
				return nil, fmt.Errorf("stream validation failed: %w", err)
			}
		}
	}

	// 内部格式 → 入站格式
	inStream, err := rc.inAdapter.TransformStream(ctx, internalStream)
	if err != nil {
		log.Warnf("failed to transform stream: %v", err)
		return nil, err
	}

	return inStream, nil
}

// handleResponse 处理非流式响应
func (rc *relayContext) handleResponse(ctx context.Context, response *http.Response) error {
	// 上游格式 → 内部格式
	internalResponse, err := rc.outAdapter.TransformResponse(ctx, response)
	if err != nil {
		log.Warnf("failed to transform response: %v", err)
		return fmt.Errorf("failed to transform outbound response: %w", err)
	}

	// 验证响应质量
	if rc.validator != nil {
		// 先检测空响应
		if err := rc.validator.ValidateEmptyResponse(internalResponse); err != nil {
			log.Warnf("empty response detected: %v", err)
			return fmt.Errorf("empty response detected: %w", err)
		}

		// 再检测其他质量问题
		if err := rc.validator.ValidateResponse(internalResponse); err != nil {
			log.Warnf("response validation failed: %v", err)
			return fmt.Errorf("response validation failed: %w", err)
		}
	}

	// 内部格式 → 入站格式
	inResponse, err := rc.inAdapter.TransformResponse(ctx, internalResponse)
	if err != nil {
		log.Warnf("failed to transform response: %v", err)
		return fmt.Errorf("failed to transform inbound response: %w", err)
	}

	rc.c.Data(http.StatusOK, "application/json", inResponse)
	return nil
}

// collectResponse 收集响应信息
func (rc *relayContext) collectResponse() {
	log.Infof("[DEBUG-TOKEN] collectResponse: calling GetInternalResponse on adapter %p", rc.inAdapter)
	internalResponse, err := rc.inAdapter.GetInternalResponse(rc.c.Request.Context())
	log.Infof("[DEBUG-TOKEN] collectResponse: response=%v, err=%v", internalResponse != nil, err)

	if err != nil {
		log.Warnf("[DEBUG-TOKEN] collectResponse: error getting internal response: %v", err)
		return
	}

	if internalResponse == nil {
		log.Warnf("[DEBUG-TOKEN] collectResponse: internal response is nil")
		return
	}

	if internalResponse.Usage != nil {
		log.Infof("[DEBUG-TOKEN] collectResponse: usage found - prompt=%d, completion=%d, total=%d",
			internalResponse.Usage.PromptTokens,
			internalResponse.Usage.CompletionTokens,
			internalResponse.Usage.TotalTokens)
	} else {
		log.Warnf("[DEBUG-TOKEN] collectResponse: usage is nil in response")
	}

	// 设置响应内容
	rc.metrics.SetInternalResponse(internalResponse)
}
