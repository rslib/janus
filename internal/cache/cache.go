package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

func New(addr string, ttlSeconds int) (*Cache, error) {
	client := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connecting to Valkey: %w", err)
	}

	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl == 0 {
		ttl = 1 * time.Hour
	}

	return &Cache{client: client, ttl: ttl}, nil
}

func (c *Cache) Get(ctx context.Context, model string, requestBody []byte) ([]byte, error) {
	key := c.cacheKey(model, requestBody)
	data, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	return data, err
}

func (c *Cache) Set(ctx context.Context, model string, requestBody, responseBody []byte) error {
	key := c.cacheKey(model, requestBody)
	return c.client.Set(ctx, key, responseBody, c.ttl).Err()
}

func (c *Cache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *Cache) Close() error {
	return c.client.Close()
}

// CacheStats holds cache statistics from the Valkey/Redis server.
type CacheStats struct {
	Hits       int64   `json:"hits"`
	Misses     int64   `json:"misses"`
	HitRate    float64 `json:"hit_rate"`
	MemoryUsed string  `json:"memory_used"`
	Keys       int64   `json:"keys"`
	Connected  bool    `json:"connected"`
}

// Stats returns cache statistics by querying Valkey/Redis INFO.
func (c *Cache) Stats(ctx context.Context) *CacheStats {
	stats := &CacheStats{}

	// Check connectivity
	if err := c.client.Ping(ctx).Err(); err != nil {
		return stats
	}
	stats.Connected = true

	// Parse INFO stats for hits/misses
	info, err := c.client.Info(ctx, "stats").Result()
	if err == nil {
		stats.Hits = parseInfoInt(info, "keyspace_hits")
		stats.Misses = parseInfoInt(info, "keyspace_misses")
		total := stats.Hits + stats.Misses
		if total > 0 {
			stats.HitRate = float64(stats.Hits) / float64(total)
		}
	}

	// Parse INFO memory
	memInfo, err := c.client.Info(ctx, "memory").Result()
	if err == nil {
		stats.MemoryUsed = parseInfoString(memInfo, "used_memory_human")
	}

	// Parse INFO keyspace
	ksInfo, err := c.client.Info(ctx, "keyspace").Result()
	if err == nil {
		stats.Keys = parseKeyspaceKeys(ksInfo)
	}

	return stats
}

func parseInfoInt(info, key string) int64 {
	prefix := key + ":"
	for _, line := range splitLines(info) {
		if len(line) > len(prefix) && line[:len(prefix)] == prefix {
			var v int64
			fmt.Sscanf(line[len(prefix):], "%d", &v)
			return v
		}
	}
	return 0
}

func parseInfoString(info, key string) string {
	prefix := key + ":"
	for _, line := range splitLines(info) {
		if len(line) > len(prefix) && line[:len(prefix)] == prefix {
			val := line[len(prefix):]
			// Trim trailing \r
			if len(val) > 0 && val[len(val)-1] == '\r' {
				val = val[:len(val)-1]
			}
			return val
		}
	}
	return ""
}

func parseKeyspaceKeys(info string) int64 {
	// Format: db0:keys=123,expires=45,avg_ttl=6789
	for _, line := range splitLines(info) {
		if len(line) > 3 && line[:2] == "db" {
			prefix := "keys="
			idx := -1
			for i := 0; i <= len(line)-len(prefix); i++ {
				if line[i:i+len(prefix)] == prefix {
					idx = i + len(prefix)
					break
				}
			}
			if idx >= 0 {
				end := idx
				for end < len(line) && line[end] >= '0' && line[end] <= '9' {
					end++
				}
				var v int64
				fmt.Sscanf(line[idx:end], "%d", &v)
				return v
			}
		}
	}
	return 0
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func (c *Cache) cacheKey(model string, requestBody []byte) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write(requestBody)
	return fmt.Sprintf("llm-gw:%x", h.Sum(nil))
}
