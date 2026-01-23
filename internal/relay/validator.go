package relay

import (
	"strings"
	"unicode"

	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// ResponseValidator 验证响应质量
type ResponseValidator struct {
	// 用于流式响应的累积内容
	accumulatedContent strings.Builder
	chunkCount         int
}

// NewResponseValidator 创建新的响应验证器
func NewResponseValidator() *ResponseValidator {
	return &ResponseValidator{}
}

// ValidateResponse 验证完整响应的质量
func (v *ResponseValidator) ValidateResponse(response *model.InternalLLMResponse) error {
	if response == nil {
		return nil
	}

	for _, choice := range response.Choices {
		var content string

		// 获取响应内容
		if choice.Message != nil {
			if choice.Message.Content.Content != nil {
				content = *choice.Message.Content.Content
			}
		} else if choice.Delta != nil {
			if choice.Delta.Content.Content != nil {
				content = *choice.Delta.Content.Content
			}
		}

		// 验证内容质量
		if err := v.validateContent(content); err != nil {
			return err
		}
	}

	return nil
}

// ValidateStreamChunk 验证流式响应块并累积内容
func (v *ResponseValidator) ValidateStreamChunk(chunk *model.InternalLLMResponse) error {
	if chunk == nil {
		return nil
	}

	v.chunkCount++

	// 在流式响应的早期阶段检测空响应
	// 如果前几个块都是空的,可能是空响应
	if v.chunkCount <= 3 {
		if err := v.ValidateEmptyResponse(chunk); err != nil {
			log.Warnf("Empty stream chunk detected in early stage (chunk %d): %v", v.chunkCount, err)
			return err
		}
	}

	for _, choice := range chunk.Choices {
		if choice.Delta != nil && choice.Delta.Content.Content != nil {
			content := *choice.Delta.Content.Content
			v.accumulatedContent.WriteString(content)

			// 每收到一定数量的块后进行检查
			if v.chunkCount%5 == 0 {
				accumulated := v.accumulatedContent.String()
				if err := v.validateContent(accumulated); err != nil {
					log.Warnf("Stream content validation failed after %d chunks: %v", v.chunkCount, err)
					return err
				}
			}
		}
	}

	return nil
}

// ValidateEmptyResponse 检测空响应
// 空响应的特征:
// 1. choices 数组为空
// 2. completion_tokens 为 0
// 3. 所有 choice 的内容都为空
func (v *ResponseValidator) ValidateEmptyResponse(response *model.InternalLLMResponse) error {
	if response == nil {
		return nil
	}

	// 检查 choices 是否为空
	if len(response.Choices) == 0 {
		log.Warnf("Empty response detected: choices array is empty")
		return &ResponseQualityError{
			Reason: "empty_response",
			Detail: "choices array is empty",
		}
	}

	// 检查 completion_tokens 是否为 0
	if response.Usage != nil && response.Usage.CompletionTokens == 0 {
		log.Warnf("Empty response detected: completion_tokens is 0")
		return &ResponseQualityError{
			Reason: "empty_response",
			Detail: "completion_tokens is 0",
		}
	}

	// 检查所有 choice 的内容是否都为空
	allEmpty := true
	for _, choice := range response.Choices {
		var hasContent bool

		// 检查 Message 内容
		if choice.Message != nil {
			if choice.Message.Content.Content != nil && *choice.Message.Content.Content != "" {
				hasContent = true
			}
			if len(choice.Message.Content.MultipleContent) > 0 {
				hasContent = true
			}
			if len(choice.Message.ToolCalls) > 0 {
				hasContent = true
			}
		}

		// 检查 Delta 内容
		if choice.Delta != nil {
			if choice.Delta.Content.Content != nil && *choice.Delta.Content.Content != "" {
				hasContent = true
			}
			if len(choice.Delta.Content.MultipleContent) > 0 {
				hasContent = true
			}
			if len(choice.Delta.ToolCalls) > 0 {
				hasContent = true
			}
		}

		if hasContent {
			allEmpty = false
			break
		}
	}

	if allEmpty {
		log.Warnf("Empty response detected: all choices have empty content")
		return &ResponseQualityError{
			Reason: "empty_response",
			Detail: "all choices have empty content",
		}
	}

	return nil
}

// validateContent 验证内容质量
func (v *ResponseValidator) validateContent(content string) error {
	if content == "" {
		return nil
	}

	// 检测重复词汇模式
	if err := v.detectRepeatingWords(content); err != nil {
		return err
	}

	// 检测无意义输出
	if err := v.detectGibberish(content); err != nil {
		return err
	}

	return nil
}

// detectRepeatingWords 检测重复词汇模式
func (v *ResponseValidator) detectRepeatingWords(content string) error {
	// 如果内容太短，跳过检查
	if len(content) < 100 {
		return nil
	}

	// 分词（简单按空格分割）
	words := strings.Fields(content)
	if len(words) < 20 {
		return nil
	}

	// 统计词频
	wordCount := make(map[string]int)
	for _, word := range words {
		// 标准化词汇（转小写，去除标点）
		normalized := strings.ToLower(strings.TrimFunc(word, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}))
		if normalized != "" {
			wordCount[normalized]++
		}
	}

	// 检查是否有词汇重复次数过高
	totalWords := len(words)
	for word, count := range wordCount {
		// 忽略常见停用词
		if isStopWord(word) {
			continue
		}

		// 如果某个词出现频率超过30%，认为是异常
		frequency := float64(count) / float64(totalWords)
		if frequency > 0.3 && count > 10 {
			log.Warnf("Detected repeating word pattern: '%s' appears %d times (%.1f%% of content)",
				word, count, frequency*100)
			return &ResponseQualityError{
				Reason: "model_output_collapse",
				Detail: "detected excessive word repetition",
			}
		}
	}

	// 检测连续重复的短语
	if err := v.detectRepeatingPhrases(words); err != nil {
		return err
	}

	return nil
}

// detectRepeatingPhrases 检测连续重复的短语
func (v *ResponseValidator) detectRepeatingPhrases(words []string) error {
	if len(words) < 10 {
		return nil
	}

	// 检查2-5个词的短语重复
	for phraseLen := 2; phraseLen <= 5; phraseLen++ {
		maxRepeats := 0
		var repeatedPhrase string

		for i := 0; i <= len(words)-phraseLen*2; i++ {
			phrase := strings.Join(words[i:i+phraseLen], " ")
			repeats := 1

			// 检查后续是否有连续重复
			for j := i + phraseLen; j <= len(words)-phraseLen; j += phraseLen {
				nextPhrase := strings.Join(words[j:j+phraseLen], " ")
				if phrase == nextPhrase {
					repeats++
				} else {
					break
				}
			}

			if repeats > maxRepeats {
				maxRepeats = repeats
				repeatedPhrase = phrase
			}
		}

		// 如果短语连续重复超过3次，认为是异常
		if maxRepeats >= 3 {
			log.Warnf("Detected repeating phrase pattern: '%s' repeats %d times consecutively",
				repeatedPhrase, maxRepeats)
			return &ResponseQualityError{
				Reason: "model_output_collapse",
				Detail: "detected consecutive phrase repetition",
			}
		}
	}

	return nil
}

// detectGibberish 检测无意义输出
func (v *ResponseValidator) detectGibberish(content string) error {
	// 如果内容太短，跳过检查
	if len(content) < 50 {
		return nil
	}

	// 检查是否包含过多的非字母字符
	letterCount := 0
	totalCount := 0
	for _, r := range content {
		if !unicode.IsSpace(r) {
			totalCount++
			if unicode.IsLetter(r) {
				letterCount++
			}
		}
	}

	if totalCount > 0 {
		letterRatio := float64(letterCount) / float64(totalCount)
		// 如果字母占比低于40%，可能是乱码
		if letterRatio < 0.4 {
			log.Warnf("Detected potential gibberish: letter ratio %.1f%% is too low", letterRatio*100)
			return &ResponseQualityError{
				Reason: "invalid_content",
				Detail: "content contains too many non-letter characters",
			}
		}
	}

	return nil
}

// isStopWord 判断是否为常见停用词
func isStopWord(word string) bool {
	stopWords := map[string]bool{
		// 英文停用词
		"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
		"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
		"with": true, "by": true, "from": true, "as": true, "is": true, "was": true,
		"are": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true, "did": true,
		"will": true, "would": true, "could": true, "should": true, "may": true, "might": true,
		"can": true, "this": true, "that": true, "these": true, "those": true,
		"i": true, "you": true, "he": true, "she": true, "it": true, "we": true, "they": true,
		// 中文停用词
		"的": true, "了": true, "在": true, "是": true, "我": true, "有": true, "和": true,
		"就": true, "不": true, "人": true, "都": true, "一": true, "一个": true, "上": true,
		"也": true, "很": true, "到": true, "说": true, "要": true, "去": true, "你": true,
		"会": true, "着": true, "没有": true, "看": true, "好": true, "自己": true, "这": true,
	}
	return stopWords[word]
}

// ResponseQualityError 响应质量错误
type ResponseQualityError struct {
	Reason string
	Detail string
}

func (e *ResponseQualityError) Error() string {
	return "response quality check failed: " + e.Reason + " - " + e.Detail
}

// IsResponseQualityError 判断是否为响应质量错误
func IsResponseQualityError(err error) bool {
	_, ok := err.(*ResponseQualityError)
	return ok
}
