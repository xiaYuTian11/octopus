package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/utils/log"
)

type ChatOutbound struct{}

func (o *ChatOutbound) TransformRequest(ctx context.Context, request *model.InternalLLMRequest, baseUrl, key string) (*http.Request, error) {
	request.ClearHelpFields()

	// Convert developer role to system role for compatibility
	for i := range request.Messages {
		if request.Messages[i].Role == "developer" {
			request.Messages[i].Role = "system"
		}
	}

	// o 系列模型不再接受 max_tokens，自动迁移到 max_completion_tokens
	if request.MaxCompletionTokens == nil && request.MaxTokens != nil && isOModel(request.Model) {
		request.MaxCompletionTokens = request.MaxTokens
		request.MaxTokens = nil
	}

	if request.Stream != nil && *request.Stream {
		if request.StreamOptions == nil {
			request.StreamOptions = &model.StreamOptions{IncludeUsage: true}
		} else if !request.StreamOptions.IncludeUsage {
			request.StreamOptions.IncludeUsage = true
		}
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	// 对于流式请求，需要设置 Accept: text/event-stream 以支持 SSE
	// 同时保留 application/json 以兼容某些返回 JSON 错误的上游
	if request.Stream != nil && *request.Stream {
		req.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	// 设置浏览器 User-Agent 以绕过 Cloudflare 等防护
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	// 设置认证 header
	// 注意：copyHeaders 会在后面完全覆盖所有 header，包括这里设置的 Authorization
	// 所以如果客户端发送了自定义的认证 header（如 x-api-key），它会被保留
	// 这里设置的 Authorization 只是作为默认值
	req.Header.Set("Authorization", "Bearer "+key)

	parsedUrl, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}

	// 根据原始 API 格式决定使用哪个端点
	// 如果客户端使用 /v1/responses，则转发到上游的 /responses
	// 否则使用标准的 /chat/completions
	var endpoint string
	if request.RawAPIFormat == model.APIFormatOpenAIResponse {
		endpoint = "/responses"
		parsedUrl.Path = parsedUrl.Path + endpoint
		log.Infof("[ENDPOINT-ROUTING] Detected Responses API format, routing to: %s", parsedUrl.String())
	} else {
		endpoint = "/chat/completions"
		parsedUrl.Path = parsedUrl.Path + endpoint
		log.Infof("[ENDPOINT-ROUTING] Using standard Chat Completions format, routing to: %s", parsedUrl.String())
	}

	// 记录请求的关键信息
	log.Infof("[OUTBOUND-REQUEST] Model: %s, BaseURL: %s, Endpoint: %s, FullURL: %s, RawAPIFormat: %s",
		request.Model, baseUrl, endpoint, parsedUrl.String(), request.RawAPIFormat)

	req.URL = parsedUrl
	req.Method = http.MethodPost
	return req, nil
}

// isOModel checks if model is o-series / gpt-4.1 family that requires max_completion_tokens.
func isOModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "gpt-4.1") {
		return true
	}
	// o1 / o3 / o4 系列
	if strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") || strings.HasPrefix(name, "o4") {
		return true
	}
	return false
}

func (o *ChatOutbound) TransformResponse(ctx context.Context, response *http.Response) (*model.InternalLLMResponse, error) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("response body is empty")
	}

	var resp model.InternalLLMResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return &resp, nil
}

func (o *ChatOutbound) TransformStream(ctx context.Context, eventData []byte) (*model.InternalLLMResponse, error) {
	if bytes.HasPrefix(eventData, []byte("[DONE]")) {
		return &model.InternalLLMResponse{
			Object: "[DONE]",
		}, nil
	}

	var errCheck struct {
		Error *model.ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(eventData, &errCheck); err == nil && errCheck.Error != nil {
		return nil, &model.ResponseError{
			Detail: *errCheck.Error,
		}
	}

	var resp model.InternalLLMResponse
	if err := json.Unmarshal(eventData, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
	}
	return &resp, nil
}
