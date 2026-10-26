// Package logging writes one JSON object per line: {"ts","level","msg",...fields}.
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Level int32

const (
	DEBUG Level = 10
	INFO  Level = 20
	WARN  Level = 30
	ERROR Level = 40
)

var names = map[Level]string{DEBUG: "DEBUG", INFO: "INFO", WARN: "WARNING", ERROR: "ERROR"}

func ParseLevel(s string) (Level, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return DEBUG, nil
	case "INFO":
		return INFO, nil
	case "WARNING", "WARN":
		return WARN, nil
	case "ERROR":
		return ERROR, nil
	}
	return INFO, fmt.Errorf("unknown level %q", s)
}

func (l Level) String() string { return names[l] }

// F is an ordered list of key/value fields.
type F []any

type Logger struct {
	mu    sync.Mutex
	w     io.Writer
	level atomic.Int32
	now   func() time.Time
}

func New(w io.Writer, lv Level) *Logger {
	l := &Logger{w: w, now: time.Now}
	l.level.Store(int32(lv))
	return l
}

func (l *Logger) SetLevel(lv Level) { l.level.Store(int32(lv)) }
func (l *Logger) Level() Level      { return Level(l.level.Load()) }

func (l *Logger) Log(lv Level, msg string, kv ...any) {
	if lv < l.Level() {
		return
	}
	var b strings.Builder
	b.WriteString(`{"ts":`)
	writeJSON(&b, l.now().Format("2006-01-02T15:04:05-0700"))
	b.WriteString(`,"level":`)
	writeJSON(&b, lv.String())
	b.WriteString(`,"msg":`)
	writeJSON(&b, msg)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteByte(',')
		writeJSON(&b, fmt.Sprint(kv[i]))
		b.WriteByte(':')
		writeJSON(&b, kv[i+1])
	}
	b.WriteString("}\n")
	l.mu.Lock()
	io.WriteString(l.w, b.String())
	l.mu.Unlock()
}

func writeJSON(b *strings.Builder, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep "<key>" readable, like Python's json.dumps
	if enc.Encode(v) != nil {
		buf.Reset()
		enc.Encode(fmt.Sprint(v))
	}
	b.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

func (l *Logger) Debug(msg string, kv ...any) { l.Log(DEBUG, msg, kv...) }
func (l *Logger) Info(msg string, kv ...any)  { l.Log(INFO, msg, kv...) }
func (l *Logger) Warn(msg string, kv ...any)  { l.Log(WARN, msg, kv...) }
func (l *Logger) Error(msg string, kv ...any) { l.Log(ERROR, msg, kv...) }
