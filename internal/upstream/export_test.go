package upstream

import "context"

// NewPollerWithResolver is NewPoller with a resolver of SRV records, such as a
// fake one.
var NewPollerWithResolver = newPoller

func (p *Poller) Resolve(ctx context.Context) (string, error) {
	return p.resolve(ctx)
}
