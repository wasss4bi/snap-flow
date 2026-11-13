package ratelimit

import (
	"context"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"time"
)

const MaxFailures = 5
const Window = 10 * time.Minute

// Pending requests have a short lease so a crashed process cannot block an IP indefinitely.
const pendingLease = 30 * time.Second

type Limiter struct{ client *redis.Client }

func New(c *redis.Client) *Limiter { return &Limiter{c} }

var beginScript = redis.NewScript(`
local now=tonumber(ARGV[1]); local window=tonumber(ARGV[2]); local lease=tonumber(ARGV[3]);
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now-window); redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',now-lease);
local failures=redis.call('ZCARD',KEYS[1]); local pending=redis.call('ZCARD',KEYS[2]);
if failures+pending>=5 then local oldest=redis.call('ZRANGE',KEYS[1],0,0,'WITHSCORES'); local wait=lease; if #oldest>0 then wait=math.max(1,tonumber(oldest[2])+window-now) end; return {0,failures,wait} end;
redis.call('ZADD',KEYS[2],now,ARGV[4]); redis.call('PEXPIRE',KEYS[2],lease); return {1,failures,0}`)
var finishScript = redis.NewScript(`
local removed=redis.call('ZREM',KEYS[2],ARGV[1]); if removed==0 then return -1 end;
if ARGV[2]=='failure' then redis.call('ZADD',KEYS[1],ARGV[3],ARGV[1]); redis.call('PEXPIRE',KEYS[1],ARGV[4]); elseif ARGV[2]=='success' then redis.call('DEL',KEYS[1]) end;
return redis.call('ZCARD',KEYS[1])`)

func keys(ip string) []string { return []string{"login:fail:" + ip, "login:pending:" + ip} }
func (l *Limiter) Begin(ctx context.Context, ip string) (string, int, time.Duration, error) {
	token := uuid.NewString()
	v, err := beginScript.Run(ctx, l.client, keys(ip), time.Now().UnixMilli(), Window.Milliseconds(), pendingLease.Milliseconds(), token).Int64Slice()
	if err != nil {
		return "", 0, 0, err
	}
	if v[0] == 0 {
		return "", int(v[1]), time.Duration(v[2]) * time.Millisecond, nil
	}
	return token, int(v[1]), 0, nil
}
func (l *Limiter) Finish(ctx context.Context, ip, token, result string) (int, error) {
	return finishScript.Run(ctx, l.client, keys(ip), token, result, time.Now().UnixMilli(), Window.Milliseconds()).Int()
}
