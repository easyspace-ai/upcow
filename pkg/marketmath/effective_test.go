package marketmath

import "testing"

func TestGetEffectivePrices(t *testing.T) {
	tob := TopOfBook{
		YesBidPips: 5500, // 0.55
		YesAskPips: 5600, // 0.56
		NoBidPips:  4700, // 0.47
		NoAskPips:  4800, // 0.48
	}
	eff, err := GetEffectivePrices(tob)
	if err != nil {
		t.Fatalf("GetEffectivePrices error: %v", err)
	}
	// effectiveBuyYes = min(0.56, 1-0.47=0.53) => 0.53
	if eff.EffectiveBuyYesPips != 5300 {
		t.Fatalf("EffectiveBuyYesPips got=%d want=%d", eff.EffectiveBuyYesPips, 5300)
	}
	// effectiveBuyNo = min(0.48, 1-0.55=0.45) => 0.45
	if eff.EffectiveBuyNoPips != 4500 {
		t.Fatalf("EffectiveBuyNoPips got=%d want=%d", eff.EffectiveBuyNoPips, 4500)
	}
	// effectiveSellYes = max(0.55, 1-0.48=0.52) => 0.55
	if eff.EffectiveSellYesPips != 5500 {
		t.Fatalf("EffectiveSellYesPips got=%d want=%d", eff.EffectiveSellYesPips, 5500)
	}
	// effectiveSellNo = max(0.47, 1-0.56=0.44) => 0.47
	if eff.EffectiveSellNoPips != 4700 {
		t.Fatalf("EffectiveSellNoPips got=%d want=%d", eff.EffectiveSellNoPips, 4700)
	}
}

func TestCheckArbitrage_Long(t *testing.T) {
	// Construct a long arb: effective buy prices sum to < 1.
	// Example: yesAsk 0.49, noAsk 0.49, bids make mirror even better:
	tob := TopOfBook{
		YesBidPips: 5200, // mirror for buy NO => 0.48
		YesAskPips: 4900,
		NoBidPips:  5200, // mirror for buy YES => 0.48
		NoAskPips:  4900,
	}
	arb, err := CheckArbitrage(tob)
	if err != nil {
		t.Fatalf("CheckArbitrage error: %v", err)
	}
	if arb == nil || arb.Type != "long" {
		t.Fatalf("expected long arb, got %+v", arb)
	}
	// effective buys: 0.48 + 0.48 = 0.96 => profit 0.04 (400 pips)
	if arb.ProfitPips != 400 {
		t.Fatalf("profit got=%d want=%d", arb.ProfitPips, 400)
	}
}

