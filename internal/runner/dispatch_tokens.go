package runner

import (
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/cde"
	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// resolveCombTokens performs Comb template substitution on the resolved
// prompt. Three token shapes are accepted:
//
//	{comb.region}             — uses node.CombVantage (region form)
//	{comb.d1=0;d2=3}          — literal region key inline
//	{comb.forager:optimist}    — direct lookup of a forager vantage
//
// Resolution goes through comb.Resolve, which performs a most-specific →
// most-general fallback for region keys and a direct lookup for forager
// vantages. Substitution failures (malformed keys, DB errors, missing
// vantages) silently produce empty strings; never blocks dispatch. Only
// the tokens the node's template left are read (node.Placeholders), so a
// token inside a context pack or a model's output stays as written. It
// returns the prompt and the placeholders still left.
func resolveCombTokens(node workflow.DispatchNode, store *db.Store) (string, []workflow.Placeholder) {
	return workflow.FillLeft(node.ResolvedPrompt, node.Placeholders, func(key string) (string, bool) {
		return combTokenValue(store, node.CombVantage, key)
	})
}

// combTokenValue is the value of the token key when it is a {comb.…}
// token, and whether it is one: {comb.region} resolves the node's
// vantage, any other key the key itself.
func combTokenValue(store *db.Store, vantage, key string) (string, bool) {
	k, ok := strings.CutPrefix(key, "comb.")
	switch {
	case !ok:
		return "", false
	case k != "region":
		return comb.Resolve(store, k), true
	case vantage == "":
		// No CombVantage — collapse to empty so the literal token
		// doesn't leak through to the agent.
		return "", true
	}
	return comb.Resolve(store, vantage), true
}

// resolveCDETokens substitutes CDE analysis tokens into a prompt:
//
//	{cde.axis-candidates}  — the axis-suggester's evidence (which
//	                         non-coordinate attributes distinguish findings
//	                         within a coordinate cell), fed to the
//	                         framer. Resolves to an honest "none"
//	                         line on an empty workspace. Best-effort: any
//	                         error collapses to "" rather than blocking.
//
// Only the placeholders the template left are read (left, as
// resolveCombTokens), and it returns those still left.
func resolveCDETokens(prompt string, left []workflow.Placeholder, store *db.Store) (string, []workflow.Placeholder) {
	var (
		text string
		done bool
	)
	return workflow.FillLeft(prompt, left, func(key string) (string, bool) {
		if key != "cde.axis-candidates" {
			return "", false
		}
		if !done {
			done = true
			if cands, err := cde.SuggestAxes(store.ReadConn(), 2); err != nil {
				text = "(axis-candidate analysis unavailable)"
			} else {
				text = cde.FormatAxisCandidates(cands)
			}
		}
		return text, true
	})
}
