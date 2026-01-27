package op

import (
	"context"
	"fmt"
	"time"
)

const cacheRefreshTimeout = 30 * time.Second

func runCacheStep(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), cacheRefreshTimeout)
	defer cancel()
	return fn(ctx)
}

func InitCache() error {
	if err := runCacheStep(settingRefreshCache); err != nil {
		return fmt.Errorf("setting refresh cache error: %v", err)
	}
	if err := runCacheStep(channelRefreshCache); err != nil {
		return fmt.Errorf("channel refresh cache error: %v", err)
	}
	if err := runCacheStep(groupRefreshCache); err != nil {
		return fmt.Errorf("group refresh cache error: %v", err)
	}
	if err := runCacheStep(apiKeyRefreshCache); err != nil {
		return fmt.Errorf("api key refresh cache error: %v", err)
	}
	if err := runCacheStep(llmRefreshCache); err != nil {
		return fmt.Errorf("llm refresh cache error: %v", err)
	}
	if err := runCacheStep(statsRefreshCache); err != nil {
		return fmt.Errorf("stats refresh cache error: %v", err)
	}
	return nil
}

func SaveCache() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := StatsSaveDB(ctx); err != nil {
		return err
	}
	if err := ChannelKeySaveDB(ctx); err != nil {
		return err
	}
	if err := RelayLogSaveDBTask(ctx); err != nil {
		return err
	}
	return nil
}
