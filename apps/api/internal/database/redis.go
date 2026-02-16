package database

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dublyo/mailat/api/internal/config"
)

var Redis *redis.Client

// ConnectRedis establishes a connection to Redis
func ConnectRedis(cfg *config.Config) (*redis.Client, error) {
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis URL: %w", err)
	}

	client := redis.NewClient(opt)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	Redis = client
	return client, nil
}

// CloseRedis closes the Redis connection
func CloseRedis() error {
	if Redis != nil {
		return Redis.Close()
	}
	return nil
}
