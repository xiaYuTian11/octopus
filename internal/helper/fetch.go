package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

// 浏览器 Headers，用于绕过基本的 Cloudflare 检测
const (
	browserUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	browserAccept     = "application/json, text/plain, */*"
	browserAcceptLang = "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7"
)

// setBrowserHeaders 为请求设置浏览器 Headers，帮助绕过基本的 Cloudflare 检测
func setBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", browserAccept)
	req.Header.Set("Accept-Language", browserAcceptLang)
}

func FetchModels(ctx context.Context, request model.Channel) ([]string, error) {
	// 验证渠道密钥
	key, err := request.GetChannelKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel key: %w", err)
	}
	if key.ChannelKey == "" {
		return nil, fmt.Errorf("channel key is empty")
	}

	client, err := ChannelHttpClient(&request)
	if err != nil {
		return nil, err
	}
	switch request.Type {
	case outbound.OutboundTypeAnthropic:
		return fetchAnthropicModels(client, ctx, request)
	case outbound.OutboundTypeGemini:
		return fetchGeminiModels(client, ctx, request)
	default:
		return fetchOpenAIModels(client, ctx, request)
	}
}

// isCloudflareChallenge 检测响应是否为 Cloudflare 挑战页面
func isCloudflareChallenge(body string) bool {
	// Cloudflare 挑战页面的常见特征
	cloudflareIndicators := []string{
		"Just a moment",
		"Cloudflare",
		"cf-browser-verification",
		"challenge-platform",
		"_cf_chl",
		"Checking your browser",
		"DDoS protection by",
	}

	for _, indicator := range cloudflareIndicators {
		if strings.Contains(body, indicator) {
			return true
		}
	}
	return false
}

// parseJSONResponse 解析 JSON 响应，如果响应不是有效的 JSON，返回更友好的错误信息
func parseJSONResponse(resp *http.Response, result interface{}) error {
	// 先读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	bodyStr := string(body)

	// 检查是否为 Cloudflare 挑战页面
	if isCloudflareChallenge(bodyStr) {
		return fmt.Errorf("API is protected by Cloudflare and cannot be accessed automatically. Please configure models manually / API 被 Cloudflare 保护，无法自动获取模型列表，请手动设置模型")
	}

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		// 尝试解析错误响应
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &errResp) == nil {
			if errResp.Error.Message != "" {
				return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
			}
			if errResp.Message != "" {
				return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, errResp.Message)
			}
		}
		// 如果无法解析错误响应，返回原始响应体的前 200 个字符
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, bodyStr)
	}

	// 尝试解析 JSON
	if err := json.Unmarshal(body, result); err != nil {
		// 如果解析失败，检查是否是 HTML 响应
		if strings.HasPrefix(strings.TrimSpace(bodyStr), "<") {
			return fmt.Errorf("received HTML response instead of JSON, the API endpoint may be incorrect or unavailable")
		}
		// 返回更详细的错误信息
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return fmt.Errorf("failed to parse JSON response: %w, body: %s", err, bodyStr)
	}

	return nil
}

// refer: https://platform.openai.com/docs/api-reference/models/list
func fetchOpenAIModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	key, err := request.GetChannelKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel key: %w", err)
	}

	req, _ := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		request.GetBaseUrl()+"/models",
		nil,
	)
	setBrowserHeaders(req)
	req.Header.Set("Authorization", "Bearer "+key.ChannelKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result model.OpenAIModelList
	if err := parseJSONResponse(resp, &result); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}

// refer: https://ai.google.dev/api/models
func fetchGeminiModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	key, err := request.GetChannelKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel key: %w", err)
	}

	var allModels []string
	pageToken := ""

	for {
		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			request.GetBaseUrl()+"/models",
			nil,
		)
		setBrowserHeaders(req)
		req.Header.Set("X-Goog-Api-Key", key.ChannelKey)

		if pageToken != "" {
			q := req.URL.Query()
			q.Add("pageToken", pageToken)
			req.URL.RawQuery = q.Encode()
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		var result model.GeminiModelList
		if err := parseJSONResponse(resp, &result); err != nil {
			return nil, err
		}

		for _, m := range result.Models {
			name := strings.TrimPrefix(m.Name, "models/")
			allModels = append(allModels, name)
		}

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}
	if len(allModels) == 0 {
		return fetchOpenAIModels(client, ctx, request)
	}
	return allModels, nil
}

// refer: https://platform.claude.com/docs
func fetchAnthropicModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	key, err := request.GetChannelKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel key: %w", err)
	}

	var allModels []string
	var afterID string
	for {

		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			request.GetBaseUrl()+"/models",
			nil,
		)
		setBrowserHeaders(req)
		req.Header.Set("X-Api-Key", key.ChannelKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")

		// 设置多页参数
		q := req.URL.Query()

		if afterID != "" {
			q.Set("after_id", afterID)
		}
		req.URL.RawQuery = q.Encode()

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		var result model.AnthropicModelList
		if err := parseJSONResponse(resp, &result); err != nil {
			return nil, err
		}

		for _, m := range result.Data {
			allModels = append(allModels, m.ID)
		}

		if !result.HasMore {
			break
		}

		afterID = result.LastID
	}
	if len(allModels) == 0 {
		return fetchOpenAIModels(client, ctx, request)
	}
	return allModels, nil
}
