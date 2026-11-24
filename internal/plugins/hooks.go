// SPDX-License-Identifier: AGPL-3.0-or-later

package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"oblivion/sdk"
)

type actionEntry struct {
	plugin string
	fn     sdk.ActionFunc
}
type filterEntry struct {
	plugin string
	fn     sdk.FilterFunc
}

// hookBus dispatches actions and filters. Listeners run in registration order
// (= plugin init order). A panicking or failing listener never breaks the emitter.
type hookBus struct {
	log     *slog.Logger
	mu      sync.RWMutex
	actions map[string][]actionEntry
	filters map[string][]filterEntry
}

func newHookBus(log *slog.Logger) *hookBus {
	return &hookBus{log: log, actions: map[string][]actionEntry{}, filters: map[string][]filterEntry{}}
}

func (b *hookBus) onAction(plugin, hook string, fn sdk.ActionFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actions[hook] = append(b.actions[hook], actionEntry{plugin, fn})
}

func (b *hookBus) onFilter(plugin, hook string, fn sdk.FilterFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filters[hook] = append(b.filters[hook], filterEntry{plugin, fn})
}

func (b *hookBus) doAction(ctx context.Context, hook string, payload any) {
	b.mu.RLock()
	list := append([]actionEntry(nil), b.actions[hook]...)
	b.mu.RUnlock()
	for _, e := range list {
		if err := b.safeAction(ctx, e, payload); err != nil {
			b.log.Warn("action listener failed", "hook", hook, "plugin", e.plugin, "err", err)
		}
	}
}

func (b *hookBus) safeAction(ctx context.Context, e actionEntry, payload any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return e.fn(ctx, payload)
}

func (b *hookBus) applyFilters(ctx context.Context, hook string, value any) any {
	b.mu.RLock()
	list := append([]filterEntry(nil), b.filters[hook]...)
	b.mu.RUnlock()
	for _, e := range list {
		out, err := b.safeFilter(ctx, e, value)
		if err != nil {
			b.log.Warn("filter listener failed", "hook", hook, "plugin", e.plugin, "err", err)
			continue // value passes through unchanged
		}
		value = out
	}
	return value
}

func (b *hookBus) safeFilter(ctx context.Context, e filterEntry, v any) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return e.fn(ctx, v)
}
