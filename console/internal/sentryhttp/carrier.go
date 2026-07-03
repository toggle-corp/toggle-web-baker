package sentryhttp

import "context"

// carrier is a pointer slot the middleware plants in the request context so
// handlers can hand the real error back for inclusion in the 5xx event.
type carrier struct {
	msg string
	err error
}

type carrierKey struct{}

// WithCarrier returns a context with an empty error carrier planted. The
// middleware calls this for every request; handlers never need to.
func WithCarrier(ctx context.Context) context.Context {
	return context.WithValue(ctx, carrierKey{}, &carrier{})
}

// AttachError records msg and err (err may be nil for message-only detail)
// on the request's carrier so the middleware can include them in a 5xx
// event. It is a no-op when ctx has no carrier, so handlers may call it
// unconditionally.
func AttachError(ctx context.Context, msg string, err error) {
	c, ok := ctx.Value(carrierKey{}).(*carrier)
	if !ok {
		return
	}
	c.msg = msg
	c.err = err
}

// carrierFrom retrieves the carrier planted by WithCarrier, or nil.
func carrierFrom(ctx context.Context) *carrier {
	c, _ := ctx.Value(carrierKey{}).(*carrier)
	return c
}
