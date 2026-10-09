/-
  Fitch Reduction — Concrete Test Instances (Layer 0)

  These mirror the Rust test cases in `fitch.rs::tests`.
  Each theorem is proved by `decide` — the Lean 4 kernel evaluates
  the concrete computation and confirms the result.

  Source values (from Rust `Item::bat()` and `Item::iron_nails()`):
    Bat:        Dimensions(form: 75.0, density: 80.0, surface: 40.0, energy: 10.0, info: 5.0)
    Iron Nails: Dimensions(form: 85.0, density: 90.0, surface: 95.0, energy: 5.0,  info: 3.0)

  10x quantized for Lean:
    Bat:        Dimensions(750, 800, 400, 100, 50)
    Iron Nails: Dimensions(850, 900, 950, 50, 30)

  Default rules (from `CompositionRule::default_rules()`):
    Hammer: MinCount(2), MaxCount(4), DimensionRange(dim:1, 700, 1000), StabilityRequired
    Cut:    MinCount(1), MaxCount(2), DimensionRange(dim:2, 200, 1000), StabilityRequired
    Inscribe: MinCount(1), MaxCount(3), StabilityRequired

  (c) 2026 Chubby Honey Bee Inc. -- chronomancy.io WASP/CDE/MSS framework
-/
import Fitch.Basic
import Fitch.Reduction

namespace Fitch.Instance

/-! ## Test Items -/

/-- Bat: form=750, density=800, surface=400, energy=100, info=50, stable -/
private def bat : Item :=
  { dims := { form := 750, density := 800, surface := 400, energy := 100, information := 50 }
    stable := true }

/-- Iron Nails: form=850, density=900, surface=950, energy=50, info=30, stable -/
private def nails : Item :=
  { dims := { form := 850, density := 900, surface := 950, energy := 50, information := 30 }
    stable := true }

/-- Corrupted bat: same dimensions, stable=false -/
private def corrupted_bat : Item :=
  { dims := { form := 750, density := 800, surface := 400, energy := 100, information := 50 }
    stable := false }

/-- Weak item: low density, fails hammer DimensionRange -/
private def weak_item : Item :=
  { dims := { form := 300, density := 200, surface := 100, energy := 50, information := 10 }
    stable := true }

/-! ## Default Rules -/

/-- Hammer rule: 2-4 items, density in [700, 1000], all stable. -/
private def hammer_rule : CompositionRule :=
  { name := "Hammer"
    constraints := [
      .minCount 2,
      .maxCount 4,
      .dimensionRange 1 700 1000,  -- dimension 1 = density
      .stabilityRequired
    ] }

/-- Cut rule: 1-2 items, surface in [200, 1000], all stable. -/
private def cut_rule : CompositionRule :=
  { name := "Cut"
    constraints := [
      .minCount 1,
      .maxCount 2,
      .dimensionRange 2 200 1000,  -- dimension 2 = surface
      .stabilityRequired
    ] }

/-- Inscribe rule: 1-3 items, all stable. -/
private def inscribe_rule : CompositionRule :=
  { name := "Inscribe"
    constraints := [
      .minCount 1,
      .maxCount 3,
      .stabilityRequired
    ] }

/-! ## Concrete Test Theorems

Each corresponds to a Rust `#[test]` in `fitch.rs::tests`.
Proved by `decide` — the Lean 4 kernel evaluates the computation.
-/

/-- F11: Bat + nails satisfy the hammer rule.
    Mirrors `test_hammer_prerequisites`. -/
theorem bat_nails_hammer :
    fitch_reduction [bat, nails] hammer_rule = true := by decide

/-- F12: Single item fails hammer rule (MinCount 2).
    Mirrors `test_insufficient_items`. -/
theorem single_item_hammer_fails :
    fitch_reduction [bat] hammer_rule = false := by decide

/-- F13: Corrupted bat fails hammer rule (StabilityRequired).
    Mirrors `test_corrupted_item_blocks_composition`. -/
theorem corrupted_fails_hammer :
    fitch_reduction [corrupted_bat, nails] hammer_rule = false := by decide

/-- Weak item fails hammer rule (DimensionRange on density). -/
theorem weak_item_fails_hammer :
    fitch_reduction [bat, weak_item] hammer_rule = false := by decide

/-- Bat alone satisfies cut rule (surface=400 in [200,1000]). -/
theorem bat_cut_passes :
    fitch_reduction [bat] cut_rule = true := by decide

/-- Bat satisfies inscribe rule (1 item, stable). -/
theorem bat_inscribe_passes :
    fitch_reduction [bat] inscribe_rule = true := by decide

/-- Too many items fails hammer rule (MaxCount 4). -/
theorem five_items_hammer_fails :
    fitch_reduction [bat, nails, bat, nails, bat] hammer_rule = false := by decide

/-- Three items passes inscribe rule (MaxCount 3). -/
theorem three_items_inscribe_passes :
    fitch_reduction [bat, nails, bat] inscribe_rule = true := by decide

/-- Empty list fails every rule. -/
theorem empty_fails_hammer :
    fitch_reduction [] hammer_rule = false := by decide

theorem empty_fails_cut :
    fitch_reduction [] cut_rule = false := by decide

theorem empty_fails_inscribe :
    fitch_reduction [] inscribe_rule = false := by decide

end Fitch.Instance
