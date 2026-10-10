package database

import (
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode"

	"snap-flow-go/internal/observability"

	"gorm.io/gorm"
)

const metricsStartKey = "snapflow:metrics_start"

type sqlStart struct {
	time      time.Time
	operation string
}

func startSQLMetrics(tx *gorm.DB) {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	operation := "unknown"
	for {
		frame, more := frames.Next()
		if strings.HasPrefix(frame.Function, "snap-flow-go/") && !strings.HasPrefix(frame.Function, "snap-flow-go/internal/database.") {
			operation = operationName(strings.TrimPrefix(frame.Function, "snap-flow-go/"))
			break
		}
		if !more {
			break
		}
	}
	tx.Statement.Settings.Store(metricsStartKey, sqlStart{time: time.Now(), operation: operation})
}

func operationName(name string) string {
	name = strings.ReplaceAll(name, ".(*", " / ")
	name = strings.ReplaceAll(name, ").", " / ")
	name = strings.ReplaceAll(name, ".", " / ")
	var out strings.Builder
	var previous rune
	for _, r := range name {
		if unicode.IsUpper(r) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			out.WriteByte(' ')
		}
		out.WriteRune(r)
		previous = r
	}
	return out.String()
}

func finishSQLMetrics(tx *gorm.DB) {
	started, ok := tx.Statement.Settings.LoadAndDelete(metricsStartKey)
	if !ok || tx.Statement.SQL.Len() == 0 {
		return
	}
	if start, ok := started.(sqlStart); ok {
		observability.ObserveSQL(tx.Statement.SQL.String(), start.operation, time.Since(start.time))
	}
}

func registerMetrics(db *gorm.DB) error {
	register := []struct {
		name string
		add  func(string, func(*gorm.DB)) error
	}{
		{"query_before", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Query().Before("*").Register(name, fn)
		}},
		{"query_after", func(name string, fn func(*gorm.DB)) error { return db.Callback().Query().After("*").Register(name, fn) }},
		{"create_before", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Create().Before("*").Register(name, fn)
		}},
		{"create_after", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Create().After("*").Register(name, fn)
		}},
		{"update_before", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Update().Before("*").Register(name, fn)
		}},
		{"update_after", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Update().After("*").Register(name, fn)
		}},
		{"delete_before", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Delete().Before("*").Register(name, fn)
		}},
		{"delete_after", func(name string, fn func(*gorm.DB)) error {
			return db.Callback().Delete().After("*").Register(name, fn)
		}},
		{"row_before", func(name string, fn func(*gorm.DB)) error { return db.Callback().Row().Before("*").Register(name, fn) }},
		{"row_after", func(name string, fn func(*gorm.DB)) error { return db.Callback().Row().After("*").Register(name, fn) }},
		{"raw_before", func(name string, fn func(*gorm.DB)) error { return db.Callback().Raw().Before("*").Register(name, fn) }},
		{"raw_after", func(name string, fn func(*gorm.DB)) error { return db.Callback().Raw().After("*").Register(name, fn) }},
	}
	for i, item := range register {
		fn := startSQLMetrics
		if i%2 == 1 {
			fn = finishSQLMetrics
		}
		if err := item.add("snapflow:metrics_"+item.name, fn); err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
	}
	return nil
}
