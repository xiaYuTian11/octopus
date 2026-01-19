# 故障转移逻辑改进文档

## 问题背景

在使用 Claude API 等大模型服务时,可能会遇到模型输出崩溃的情况,表现为:
- 响应中包含大量重复的无意义词汇(如 "unclear"、"rays"、"updating" 等)
- HTTP 状态码仍然是 200,但响应内容无效
- 现有的故障转移逻辑无法识别这种"成功但无效"的响应

## 改进方案

### 1. 新增响应质量验证器 ([`internal/relay/validator.go`](../internal/relay/validator.go))

创建了 `ResponseValidator` 结构体,用于检测响应质量问题:

#### 主要功能:
- **重复词汇检测**: 统计词频,如果某个词出现频率超过 30% 且出现次数超过 10 次,判定为异常
- **连续短语重复检测**: 检测 2-5 个词的短语是否连续重复 3 次以上
- **无意义输出检测**: 检查字母占比,如果低于 40%,可能是乱码
- **流式响应累积验证**: 每收到 5 个流式块后进行一次质量检查

#### 核心方法:
```go
// ValidateResponse - 验证完整响应的质量
func (v *ResponseValidator) ValidateResponse(response *model.InternalLLMResponse) error

// ValidateStreamChunk - 验证流式响应块并累积内容
func (v *ResponseValidator) ValidateStreamChunk(chunk *model.InternalLLMResponse) error

// detectRepeatingWords - 检测重复词汇模式
func (v *ResponseValidator) detectRepeatingWords(content string) error

// detectRepeatingPhrases - 检测连续重复的短语
func (v *ResponseValidator) detectRepeatingPhrases(words []string) error

// detectGibberish - 检测无意义输出
func (v *ResponseValidator) detectGibberish(content string) error
```

#### 错误类型:
```go
type ResponseQualityError struct {
    Reason string // "model_output_collapse" 或 "invalid_content"
    Detail string // 详细错误信息
}
```

### 2. 集成到中继逻辑 ([`internal/relay/relay.go`](../internal/relay/relay.go))

#### 修改点 1: 在 relayContext 中添加验证器
在 [`internal/relay/type.go`](../internal/relay/type.go:57) 的 `relayContext` 结构体中添加:
```go
// validator: 响应质量验证器,用于检测模型输出崩溃等异常
validator *ResponseValidator
```

#### 修改点 2: 初始化验证器
在创建 `relayContext` 时初始化验证器 ([`relay.go:107`](../internal/relay/relay.go:107)):
```go
rc := &relayContext{
    // ... 其他字段
    validator: NewResponseValidator(),
}
```

#### 修改点 3: 非流式响应验证
在 [`handleResponse`](../internal/relay/relay.go:372) 函数中添加验证逻辑:
```go
// 验证响应质量
if rc.validator != nil {
    if err := rc.validator.ValidateResponse(internalResponse); err != nil {
        log.Warnf("response validation failed: %v", err)
        return fmt.Errorf("response validation failed: %w", err)
    }
}
```

#### 修改点 4: 流式响应验证
在 [`transformStreamData`](../internal/relay/relay.go:343) 函数中添加验证逻辑:
```go
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
```

#### 修改点 5: 流式响应早期中断
在 [`handleStreamResponse`](../internal/relay/relay.go:314) 函数中改进错误处理:
```go
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
```

## 工作流程

### 非流式响应:
1. 接收上游完整响应
2. 转换为内部格式
3. **验证响应质量** ← 新增
4. 如果验证失败,返回错误,触发故障转移
5. 如果验证成功,转换为入站格式并返回客户端

### 流式响应:
1. 接收上游流式响应块
2. 转换为内部格式
3. **累积内容并定期验证质量** ← 新增
4. 如果验证失败且未写入客户端,中断流并触发故障转移
5. 如果验证成功,转换为入站格式并写入客户端

## 故障转移触发条件

现在系统会在以下情况下自动切换到下一个渠道:

1. **HTTP 错误** (原有): 状态码不在 200-299 范围
2. **网络错误** (原有): 请求发送失败
3. **首个 Token 超时** (原有): 流式响应超时未产生输出
4. **响应质量错误** (新增): 检测到模型输出崩溃
   - 重复词汇过多
   - 连续短语重复
   - 无意义输出(乱码)

## 配置参数

当前验证器使用的阈值:

| 参数 | 值 | 说明 |
|------|-----|------|
| 最小内容长度(重复检测) | 100 字符 | 内容太短时跳过检查 |
| 最小词数(重复检测) | 20 个词 | 词数太少时跳过检查 |
| 词频阈值 | 30% | 单词出现频率超过此值视为异常 |
| 最小重复次数 | 10 次 | 单词重复次数超过此值才检查频率 |
| 短语连续重复阈值 | 3 次 | 短语连续重复超过此值视为异常 |
| 字母占比阈值 | 40% | 字母占比低于此值视为乱码 |
| 流式验证间隔 | 每 5 个块 | 流式响应每收到 5 个块检查一次 |

## 日志输出

改进后的系统会输出以下日志:

```
# 检测到重复词汇
[WARN] Detected repeating word pattern: 'unclear' appears 150 times (45.2% of content)

# 检测到短语重复
[WARN] Detected repeating phrase pattern: 'unclear unclear' repeats 5 times consecutively

# 检测到乱码
[WARN] Detected potential gibberish: letter ratio 25.3% is too low

# 流式验证失败,切换渠道
[WARN] stream validation failed before client write, will retry next channel: response quality check failed: model_output_collapse - detected excessive word repetition

# 响应验证失败
[WARN] response validation failed: response quality check failed: model_output_collapse - detected consecutive phrase repetition
```

## 测试建议

### 1. 模拟模型输出崩溃
创建一个测试渠道,返回包含大量重复词汇的响应,验证系统是否能正确识别并切换到下一个渠道。

### 2. 正常响应测试
确保正常的响应不会被误判为异常,特别是:
- 技术文档中的专业术语重复
- 代码示例中的关键字重复
- 列表或表格中的结构化内容

### 3. 流式响应测试
测试流式响应在不同阶段检测到异常的情况:
- 早期检测(未写入客户端) - 应该能切换渠道
- 中期检测(已写入部分内容) - 应该记录错误但继续处理

### 4. 性能测试
验证响应验证不会显著影响系统性能:
- 测量添加验证前后的响应时间
- 监控 CPU 和内存使用情况

## 未来改进方向

1. **可配置的阈值**: 允许通过配置文件调整验证参数
2. **更智能的检测**: 使用机器学习模型识别异常响应
3. **统计分析**: 收集异常响应数据,分析模式
4. **自适应调整**: 根据历史数据自动调整阈值
5. **多语言支持**: 改进对不同语言的停用词处理

## 相关文件

- [`internal/relay/validator.go`](../internal/relay/validator.go) - 响应质量验证器
- [`internal/relay/relay.go`](../internal/relay/relay.go) - 中继逻辑主文件
- [`internal/relay/type.go`](../internal/relay/type.go) - 类型定义
- [`internal/relay/balancer/balancer.go`](../internal/relay/balancer/balancer.go) - 负载均衡器

## 版本信息

- 改进日期: 2026-01-19
- 改进版本: v1.0
- 影响范围: 故障转移逻辑、响应验证
