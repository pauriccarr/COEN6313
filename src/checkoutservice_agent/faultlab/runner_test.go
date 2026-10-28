package faultlab

import (
	"slices"
	"testing"

	"checkoutservice-agent/agent"
)

// What each mode must leave behind on the mock backend: the infection point,
// a checkpoint it must lose ("" = none), and a correctness rule that must
// fail ("" = the fault goes unnoticed by the rules).
var want = map[string]struct{ infection, lost, rule string }{
	"FM_1_2":              {"ORDER_SHIPPED", "", "paid_before_ship"},
	"FM_1_3":              {"PAYMENT_CHARGED", "", "paid_once"},
	"FM_2_2":              {"PRODUCTS_LOADED", "PRODUCTS_LOADED", "charge_total"},
	"FM_2_5":              {"SHIPPING_QUOTED", "", "charge_total"},
	"FM_3_1":              {"FINAL_ANSWER", "PAYMENT_CHARGED", "paid_once"},
	"FM_3_2":              {"ORDER_VERIFIED", "ORDER_VERIFIED", ""},
	"BL_SKIP_PAYMENT":     {"PAYMENT_CHARGED", "PAYMENT_CHARGED", "paid_once"},
	"BL_FAKE_TRACKING":    {"ORDER_SHIPPED", "ORDER_SHIPPED", "shipped_once"},
	"BL_CART_NOT_EMPTIED": {"CART_EMPTIED", "CART_EMPTIED", "cart_emptied"},
	"BL_NO_CONFIRMATION":  {"CONFIRMATION_SENT", "CONFIRMATION_SENT", "confirmation"},
	"BL_WRONG_TOTAL":      {"PAYMENT_CHARGED", "", "charge_total"},
	"BL_WRONG_CURRENCY":   {"PAYMENT_CHARGED", "", "charge_total"},
	"BL_ITEM_DROPPED":     {"CART_LOADED", "", "items_match_cart"},
	"BL_DUPLICATE_ITEM":   {"CART_LOADED", "", "items_match_cart"},
	"BL_WRONG_ADDRESS":    {"ORDER_SHIPPED", "", "ship_address"},
}

func TestFaultInjection(t *testing.T) {
	t.Setenv("RESULTS_DIR", t.TempDir())
	for _, currency := range []string{"EUR", "USD"} {
		t.Run(currency, func(t *testing.T) {
			m, err := StartMock()
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			r, err := m.Runner(nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Currency = currency
			res := r.RunAll(agent.Modes)
			if len(res.Results) != len(agent.Modes) {
				t.Fatalf("got %d rows, want %d", len(res.Results), len(agent.Modes))
			}
			for _, row := range res.Results {
				if row.FaultMode == "NONE" {
					if row.Error != "" || row.InfectionPoint != "" || len(row.StepsLost) != 0 || row.PropagationDepth != 0 {
						t.Errorf("baseline not clean: %+v", row)
					}
					if row.LastKnownWell != "FINAL_ANSWER" {
						t.Errorf("baseline last known well = %q", row.LastKnownWell)
					}
					continue
				}
				w, ok := want[row.FaultMode]
				if !ok {
					t.Errorf("%s: no expectation in this test", row.FaultMode)
					continue
				}
				if row.InfectionPoint != w.infection {
					t.Errorf("%s: infection point %q, want %q", row.FaultMode, row.InfectionPoint, w.infection)
				}
				if row.PropagationDepth < 1 {
					t.Errorf("%s: propagation depth %d", row.FaultMode, row.PropagationDepth)
				}
				if w.lost != "" && !slices.Contains(row.StepsLost, w.lost) {
					t.Errorf("%s: %s not lost (lost %v)", row.FaultMode, w.lost, row.StepsLost)
				}
				if w.rule != "" && !slices.Contains(row.FailedChecks, w.rule) {
					t.Errorf("%s: rule %s did not fail (failed %v)", row.FaultMode, w.rule, row.FailedChecks)
				}
			}
		})
	}
}

func TestReorderPlanKeepsEveryStep(t *testing.T) {
	m, err := StartMock()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r, _ := m.Runner(nil)
	row := r.RunOne("FM_1_2")
	var tools []string
	for _, s := range row.Trace {
		tools = append(tools, s.Tool)
	}
	ship, charge := slices.Index(tools, "ship_order"), slices.Index(tools, "charge_card")
	if ship < 0 || charge < 0 || ship > charge {
		t.Fatalf("FM_1_2 should ship before charging: %v", tools)
	}
	if len(m.Fakes.Charges) != 1 {
		t.Errorf("charges = %d, want 1", len(m.Fakes.Charges))
	}
}
