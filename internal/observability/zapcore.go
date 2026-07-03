package observability

import (
	"github.com/getsentry/sentry-go"
	"go.uber.org/zap/zapcore"
)

// maxErrorDepth bounds how many wrapped causes SetException unwinds
// (mirrors sentry-go's own internal default).
const maxErrorDepth = 10

// NewZapCore returns a zapcore.Core that tees Error+ log entries to Sentry
// through the reporter. A nil reporter yields a no-op core, so callers can
// wire the tee unconditionally.
func NewZapCore(r *Reporter) zapcore.Core {
	if r == nil {
		return zapcore.NewNopCore()
	}
	return &sentryCore{reporter: r}
}

// sentryCore forwards Error+ entries to Sentry via the reporter's hub,
// sharing its per-fingerprint rate limiter.
type sentryCore struct {
	reporter *Reporter
	// fields accumulated through With; each With returns a clone so
	// sibling loggers never see each other's fields.
	fields []zapcore.Field
}

func (c *sentryCore) Enabled(level zapcore.Level) bool {
	return level >= zapcore.ErrorLevel
}

func (c *sentryCore) With(fields []zapcore.Field) zapcore.Core {
	clone := &sentryCore{
		reporter: c.reporter,
		fields:   make([]zapcore.Field, 0, len(c.fields)+len(fields)),
	}
	clone.fields = append(clone.fields, c.fields...)
	clone.fields = append(clone.fields, fields...)
	return clone
}

func (c *sentryCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if !c.Enabled(entry.Level) {
		return ce
	}
	return ce.AddCore(entry, c)
}

func (c *sentryCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if dropMessage(entry.Message) {
		return nil
	}
	err := extractError(c.fields, fields)
	if dropError(err) {
		return nil
	}

	fingerprint := []string{entry.LoggerName, entry.Message}
	if !c.reporter.limiter.allow(fingerprint) {
		return nil
	}

	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Message = entry.Message
	event.Fingerprint = fingerprint
	if err != nil {
		event.SetException(err, maxErrorDepth)
	}

	// Merge With-accumulated fields with call-site fields into a context.
	// (sentry-go v0.47 removed Event.Extra; a context is the replacement.)
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}
	if len(enc.Fields) > 0 {
		event.Contexts = map[string]sentry.Context{"fields": enc.Fields}
	}
	c.reporter.hub.CaptureEvent(event)
	return nil
}

// extractError pulls the last error attached via zap.Error from the
// accumulated and call-site fields (zap stores the error in
// Field.Interface for ErrorType fields).
func extractError(fieldSets ...[]zapcore.Field) error {
	var last error
	for _, fields := range fieldSets {
		for _, f := range fields {
			if f.Type != zapcore.ErrorType {
				continue
			}
			if err, ok := f.Interface.(error); ok {
				last = err
			}
		}
	}
	return last
}

// Sync returns nil immediately: zap Syncs after every Error log, and a
// sentry.Flush here would block reconciles. Flushing happens once at
// shutdown via Reporter.Flush.
func (c *sentryCore) Sync() error { return nil }
