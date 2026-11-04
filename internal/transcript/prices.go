package transcript

import "strings"

// Price is list price in USD per million tokens.
type Price struct {
	Input, Output, CacheRead, CacheWrite5m, CacheWrite1h float64
}

// Cost of u at this price.
func (p Price) Cost(u Usage) float64 {
	return (float64(u.Input)*p.Input + float64(u.Output)*p.Output + float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite5m)*p.CacheWrite5m + float64(u.CacheWrite1h)*p.CacheWrite1h) / 1e6
}

func price(in, out, read float64) Price {
	// Cache writes cost 1.25× input for the 5-minute TTL and 2× for 1 hour.
	return Price{Input: in, Output: out, CacheRead: read, CacheWrite5m: in * 1.25, CacheWrite1h: in * 2}
}

// prices by model id prefix, longest match first. Anthropic first-party list
// prices, checked 2026-11-04 (claude-opus-5-5 matches Claude Code's own
// total_cost_usd exactly). Haiku 5.5 doubles its price above 100K-token
// prompts; that tier is ignored here, as delegated prompts stay well below it.
var prices = []struct {
	prefix string
	p      Price
}{
	{"claude-fable-5-1", price(10, 50, 0.25)},
	{"claude-fable-5", price(10, 50, 1)},
	{"claude-opus-5-5", price(4, 20, 0.20)},
	{"claude-opus-5", price(5, 25, 0.50)},
	{"claude-opus-4", price(5, 25, 0.50)},
	{"claude-sonnet-5", price(2, 10, 0.20)},
	{"claude-sonnet-4", price(3, 15, 0.30)},
	{"claude-haiku-5", price(0.10, 0.50, 0.01)},
	{"claude-haiku-4", price(1, 5, 0.10)},
}

// PriceFor returns the list price of model. Unknown models get Opus 5.5's
// price and ok=false, so they are counted and flagged rather than dropped.
func PriceFor(model string) (Price, bool) {
	for _, e := range prices {
		if strings.HasPrefix(model, e.prefix) {
			return e.p, true
		}
	}
	return price(4, 20, 0.20), false
}
