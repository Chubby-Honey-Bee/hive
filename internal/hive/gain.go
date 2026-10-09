package hive

import (
	"encoding/json"
	"fmt"
	"slices"
)

// applyParamAdjustments applies the plan's adjust_params actions, in order,
// on ex, and returns the params of each one applied. The plan computes them
// from the signals (tremble_dance lowers batch_size; shaking_signal raises
// it, or upgrades the model tier). `chb hive next --apply` applies them:
// RecordScan runs this in the scan's transaction when Scan.Apply is set.
func applyParamAdjustments(ex execer, project string, actions []Action) ([]map[string]any, error) {
	var applied []map[string]any
	for _, a := range actions {
		if !adjustsParams(a) {
			continue
		}
		if err := applyGainControl(ex, project, a.Params); err != nil {
			return applied, fmt.Errorf("apply %s: %w", a.ID, err)
		}
		applied = append(applied, a.Params)
	}
	return applied, nil
}

// adjustsParams reports whether an action is an adjust_params with params.
func adjustsParams(a Action) bool {
	return a.Type == "adjust_params" && len(a.Params) > 0
}

// CheckGainParams refuses a batch_size or convergence_threshold that is not
// a number, which would otherwise read as 0, be stored as the minimum, and
// still report success.
func CheckGainParams(params map[string]any) error {
	for _, k := range []string{"batch_size", "convergence_threshold"} {
		v, present := params[k]
		if !present {
			continue
		}
		if _, ok := toInt(v); !ok {
			shown, _ := json.Marshal(v)
			return fmt.Errorf("%s must be a number, got %s", k, shown)
		}
	}
	return nil
}

// gainColumns are the hive_state columns gain control sets, in the order
// the UPDATE names them, each with how a param becomes its stored value.
var gainColumns = []struct {
	name  string
	value func(v any) any
}{
	{"batch_size", clampBatchSize},
	{"convergence_threshold", clampConvergence},
	{"model_tier", storedModelTier},
}

// clampBatchSize stores a batch_size within BatchSizeMin and BatchSizeMax.
func clampBatchSize(v any) any {
	n, _ := toInt(v)
	return clampInt(n, BatchSizeMin, BatchSizeMax)
}

// clampConvergence stores a convergence_threshold within ConvergenceMin and
// ConvergenceMax.
func clampConvergence(v any) any {
	n, _ := toInt(v)
	return clampInt(n, ConvergenceMin, ConvergenceMax)
}

// storedModelTier stores a model_tier on the ladder, and any other as the
// default tier.
func storedModelTier(v any) any {
	tier, _ := v.(string)
	if !slices.Contains(ModelTiers(), tier) {
		tier = defaultModelTier()
	}
	return tier
}

// applyGainControl applies bounded parameter adjustments to hive_state on
// an executor. It refuses a non-numeric batch_size or convergence_threshold
// and writes nothing.
func applyGainControl(ex execer, project string, params map[string]any) error {
	if err := CheckGainParams(params); err != nil {
		return err
	}
	setClauses, args := gainSetClauses(params)
	if setClauses == "" {
		return nil
	}

	setClauses += "updated_at = CURRENT_TIMESTAMP"
	args = append(args, project)

	_, err := ex.Exec(
		fmt.Sprintf("UPDATE hive_state SET %s WHERE project = ?", setClauses),
		args...,
	)
	return err
}

// gainSetClauses are the UPDATE's assignments for the params given, each
// ending ", ", and their values.
func gainSetClauses(params map[string]any) (string, []any) {
	var setClauses string
	var args []any
	for _, col := range gainColumns {
		v, ok := params[col.name]
		if !ok {
			continue
		}
		setClauses += col.name + " = ?, "
		args = append(args, col.value(v))
	}
	return setClauses, args
}

func clampInt(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

// toInt reads a number and reports whether v was one.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
