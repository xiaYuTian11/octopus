# 空响应重试逻辑修复方案

## 问题分析

### 严重问题 1: 无限重试同一渠道

在 [`internal/relay/relay.go`](internal/relay/relay.go:56-132) 中存在双层循环:

```go
const maxRounds = 3
for round := 0; round < maxRounds; round++ {
    item := b.Select(group.Items)
    
    for i := 0; i < itemCount; i++ {
        // ... 渠道处理逻辑 ...
        item = b.Next(group.Items, item)
    }
}
```

**问题**: 
- 外层循环: 最多 3 轮
- 内层循环: 遍历所有渠道
- **对于单渠道分组**: `itemCount = 1`，但外层会循环 3 次
- **对于 RoundRobin/Random/Weighted 模式**: `Next()` 方法会返回**相同或随机的渠道**，导致同一渠道被反复尝试

**实际行为**:
1. 单渠道分组: 同一渠道会被尝试 **3 次** (maxRounds)
2. RoundRobin 模式: 可能重复请求同一渠道多次
3. Random 模式: 随机选择，可能重复
4. Weighted 模式: 按权重随机，可能重复

### 问题 2: Failover 模式的特殊性

只有 **Failover 模式** 的 [`Next()`](internal/relay/balancer/balancer.go:75-86) 方法实现了正确的"下一个渠道"逻辑:

```go
func (b *Failover) Next(items []model.GroupItem, current *model.GroupItem) *model.GroupItem {
    sorted := sortByPriority(items)
    for i, item := range sorted {
        if item.ID == current.ID && i+1 < len(sorted) {
            return &sorted[i+1]  // 返回下一个渠道
        }
    }
    return nil  // 已到最后一个渠道
}
```

其他模式的 `Next()` 都是重新 `Select()`:
- [`RoundRobin.Next()`](internal/relay/balancer/balancer.go:46-48): 返回 `b.Select(items)` - 下一个轮询位置(可能重复)
- [`Random.Next()`](internal/relay/balancer/balancer.go:60-62): 返回 `b.Select(items)` - 随机选择(可能重复)
- [`Weighted.Next()`](internal/relay/balancer/balancer.go:112-114): 返回 `b.Select(items)` - 按权重随机(可能重复)

### 问题 3: 空响应检测触发重试

[`ValidateEmptyResponse()`](internal/relay/validator.go:93-162) 返回 `ResponseQualityError`，会触发重试:

```go
if err := rc.validator.ValidateEmptyResponse(internalResponse); err != nil {
    log.Warnf("empty response detected: %v", err)
    return fmt.Errorf("empty response detected: %w", err)
}
```

由于重试逻辑的问题，这会导致:
- **单渠道分组**: 同一渠道重试 3 次
- **多渠道分组**: 可能重复请求同一渠道

## 修复方案

### 方案 1: 移除外层循环，只保留一轮渠道遍历

```go
// 移除 maxRounds，每个渠道只尝试一次
itemCount := len(group.Items)
b := balancer.GetBalancer(group.Mode)
item := b.Select(group.Items)

for i := 0; i < itemCount; i++ {
    // ... 处理当前渠道 ...
    
    item = b.Next(group.Items, item)
    if item == nil {
        break  // Failover 模式中无更多渠道
    }
}
```

**优点**:
- 简单直接
- 确保每个渠道只尝试一次
- 与 Failover 模式的语义一致

**缺点**:
- RoundRobin/Random/Weighted 模式下，`Next()` 可能返回已尝试的渠道

### 方案 2: 记录已尝试的渠道 (推荐)

```go
const maxRetries = len(group.Items) * 2  // 最多尝试渠道数的 2 倍
attemptedChannels := make(map[int]bool)  // 记录已尝试的渠道 ID
b := balancer.GetBalancer(group.Mode)

for attempt := 0; attempt < maxRetries; attempt++ {
    item := b.Select(group.Items)
    if item == nil {
        break
    }
    
    // 检查是否已尝试过此渠道
    if attemptedChannels[item.ChannelID] {
        // 所有渠道都尝试过了
        if len(attemptedChannels) >= len(group.Items) {
            break
        }
        continue  // 跳过已尝试的渠道
    }
    
    attemptedChannels[item.ChannelID] = true
    
    // ... 处理渠道 ...
}
```

**优点**:
- 确保每个渠道只尝试一次
- 适用于所有负载均衡模式
- 有明确的退出条件

**缺点**:
- 需要额外的内存记录
- 代码稍复杂

### 方案 3: 改进 Next() 方法实现

为所有模式实现真正的"下一个渠道"逻辑，避免重复。

**优点**:
- 语义清晰
- 修复根本问题

**缺点**:
- 需要修改多个文件
- 改动较大

## 推荐方案: 方案 2

理由:
1. 最小化破坏性修改
2. 明确防止重复
3. 适用于所有负载均衡模式
4. 有清晰的日志可追踪

## 额外改进

### 1. 添加详细日志

```go
log.Infof("Attempting channel %s (attempt %d/%d, unique channels tried: %d/%d)",
    channel.Name, attempt+1, maxRetries, len(attemptedChannels), len(group.Items))
```

### 2. 区分空响应错误

```go
if lastErr != nil && IsResponseQualityError(lastErr) {
    errorMsg = "all channels returned empty or invalid responses"
    log.Warnf("All channels failed with quality issues after %d attempts", attempt)
}
```

### 3. 早期退出优化

```go
// 如果所有渠道都尝试过了，立即退出
if len(attemptedChannels) >= len(group.Items) {
    log.Infof("All %d channels have been attempted, stopping retry", len(group.Items))
    break
}