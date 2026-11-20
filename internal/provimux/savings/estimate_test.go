package savings

import "testing"

var opusPrice = price{Src: sourceOpenRouter, In: 4, Out: 20, Cr: fptr(0.2), Cw: fptr(5), Cw1h: fptr(8)}

// The worked example of 02-prices.md section 6.
func TestTheWorkedExample(t *testing.T) {
	c := counters{In: 710_000, Cr: 138_400_000, Cw: 6_100_000, Cw1h: 6_100_000, Out: 3_300_000}
	cost, without := amounts(c, opusPrice)
	if roundMoney(cost) != 145.32 || roundMoney(without) != 646.84 || roundMoney(without-cost) != 501.52 {
		t.Fatalf("cost %v without %v saved %v", cost, without, without-cost)
	}
	if rate := roundTo(float64(c.Cr)/float64(c.In+c.Cr+c.Cw), 1e4); rate != 0.9531 {
		t.Fatalf("hit rate %v", rate)
	}
}

func TestMissingPrices(t *testing.T) {
	c := counters{In: 1_000_000, Cr: 1_000_000, Cw: 1_000_000, Cw1h: 500_000}
	noRead := price{In: 2, Out: 10, Cw: fptr(2.5), Cw1h: fptr(4)}
	cost, without := amounts(counters{Cr: 1_000_000}, noRead)
	if cost != without || cost != 2 {
		t.Fatalf("without a cache-read price reads save nothing: %v %v", cost, without)
	}
	noWrite := price{In: 2, Out: 10, Cr: fptr(0.2)}
	cost, without = amounts(counters{Cw: 1_000_000, Cw1h: 1_000_000}, noWrite)
	if cost != without || cost != 2 {
		t.Fatalf("without a cache-write price there is no premium: %v %v", cost, without)
	}
	no1h := price{In: 2, Out: 10, Cr: fptr(0.2), Cw: fptr(2.5)}
	a, _ := amounts(c, no1h)
	b, _ := amounts(counters{In: 1_000_000, Cr: 1_000_000, Cw: 1_000_000}, no1h)
	if !near(a, b) {
		t.Fatalf("without a 1 hour price 1 hour writes are valued as cache writes: %v %v", a, b)
	}
}

func TestANegativeSaving(t *testing.T) {
	cost, without := amounts(counters{Cw: 1_000_000, Out: 1000}, opusPrice)
	if saved := without - cost; saved >= 0 || !near(saved, -1) {
		t.Fatalf("writes without reads must save a negative amount, got %v", saved)
	}
}

func TestTheSplitIsOnlyAPart(t *testing.T) {
	base := counters{In: 10, Cr: 1000, Cw: 5000, Out: 7}
	withCw5 := base
	withCw5.Cw5 = 3000
	a, aw := amounts(base, opusPrice)
	b, bw := amounts(withCw5, opusPrice)
	if a != b || aw != bw {
		t.Fatal("cw5 must change no amount")
	}
	with1h := base
	with1h.Cw1h = 2000
	c, cw := amounts(with1h, opusPrice)
	if cw != aw || !near(c-a, 2000*(8-5)/1e6) {
		t.Fatalf("cw1h moves only the 1 hour part: %v %v", c, a)
	}
}

func TestRoundingAtTheEnd(t *testing.T) {
	var sum float64
	for i := 0; i < 1000; i++ {
		cost, _ := amounts(counters{In: 1}, price{In: 0.4}) // 0.0000004 dollars
		sum += cost
	}
	if roundMoney(sum) != 0.0004 {
		t.Fatalf("sum %v rounded %v", sum, roundMoney(sum))
	}
}
