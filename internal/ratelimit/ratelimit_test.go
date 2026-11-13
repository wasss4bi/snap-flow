package ratelimit

import (
	"context"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
)

func TestRedisFailuresAndConcurrentPermits(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("requires isolated Redis")
	}
	r := redis.NewClient(&redis.Options{Addr: addr})
	defer r.Close()
	ctx := context.Background()
	l := New(r)
	ip := "198.51.100.42"
	r.Del(ctx, keys(ip)...)
	defer r.Del(ctx, keys(ip)...)
	var leases []string
	for i := 0; i < 5; i++ {
		token, _, _, err := l.Begin(ctx, ip)
		if err != nil || token == "" {
			t.Fatalf("begin %d %v", i, err)
		}
		leases = append(leases, token)
	}
	if token, _, _, err := l.Begin(ctx, ip); err != nil || token != "" {
		t.Fatalf("sixth concurrent accepted: %v", err)
	}
	for i, token := range leases {
		n, err := l.Finish(ctx, ip, token, "failure")
		if err != nil || n != i+1 {
			t.Fatalf("failure=%d err=%v", n, err)
		}
	}
	if token, _, ttl, err := l.Begin(ctx, ip); err != nil || token != "" || ttl <= 0 || ttl > Window {
		t.Fatalf("blocked: %s %v %v", token, ttl, err)
	}
	r.Del(ctx, keys(ip)...)
	token, _, _, err := l.Begin(ctx, ip)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := l.Finish(ctx, ip, token, "success"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
