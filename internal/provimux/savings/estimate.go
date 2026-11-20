package savings

import "math"

// counters are the sums kept per row of a day (schemas/day.schema.json, $defs/counters). A counter
// that is 0 is left out of the stored value.
type counters struct {
	Req   int64 `json:"req,omitempty"`
	Fail  int64 `json:"fail,omitempty"`
	In    int64 `json:"in,omitempty"`
	Cr    int64 `json:"cr,omitempty"`
	Cw    int64 `json:"cw,omitempty"`
	Cw5   int64 `json:"cw5,omitempty"`
	Cw1h  int64 `json:"cw1h,omitempty"`
	Out   int64 `json:"out,omitempty"`
	Rsn   int64 `json:"rsn,omitempty"`
	Unc   int64 `json:"unc,omitempty"`
	Cost  int64 `json:"cost,omitempty"`
	Ucost int64 `json:"ucost,omitempty"`
	Costn int64 `json:"costn,omitempty"`
}

func (c *counters) add(o counters) {
	c.Req += o.Req
	c.Fail += o.Fail
	c.In += o.In
	c.Cr += o.Cr
	c.Cw += o.Cw
	c.Cw5 += o.Cw5
	c.Cw1h += o.Cw1h
	c.Out += o.Out
	c.Rsn += o.Rsn
	c.Unc += o.Unc
	c.Cost += o.Cost
	c.Ucost += o.Ucost
	c.Costn += o.Costn
}

// tokens is every token of the counters: the three input buckets, output and unclassified.
func (c counters) tokens() int64 { return c.In + c.Cr + c.Cw + c.Out + c.Unc }

// amounts is 02-prices.md section 6: the cost and the cost without the cache, in dollars.
// The saving is withoutCache - cost: net of the cache-write premium, and negative when writes cost
// more than reads saved. No saving is claimed without a published cache-read price, and no premium
// is assumed without a published cache-write price. cw5 is not in the formula; unc is in no amount.
func amounts(c counters, p price) (cost, withoutCache float64) {
	pr := p.In
	if p.Cr != nil {
		pr = *p.Cr
	}
	pw := p.In
	if p.Cw != nil {
		pw = *p.Cw
	}
	pw1 := pw
	if p.Cw1h != nil {
		pw1 = *p.Cw1h
	}
	u, r, w, w1, o := float64(c.In), float64(c.Cr), float64(c.Cw), float64(c.Cw1h), float64(c.Out)
	cost = (u*p.In + r*pr + (w-w1)*pw + w1*pw1 + o*p.Out) / 1_000_000
	withoutCache = ((u+r+w)*p.In + o*p.Out) / 1_000_000
	return cost, withoutCache
}

// roundMoney rounds dollars to 6 decimals, at the end only.
func roundMoney(v float64) float64 { return roundTo(v, 1e6) }

func roundTo(v, scale float64) float64 {
	r := math.Round(v*scale) / scale
	if r == 0 {
		return 0 // never -0
	}
	return r
}
