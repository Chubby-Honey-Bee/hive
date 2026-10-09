#!/usr/bin/env python3
"""Join each arm's results.jsonl to its calibration lines and apply rule.md.

    decide.py <evidence-dir> <baseline-name> <candidate-name>

Reads results-<arm>.jsonl and cal-<arm>.jsonl, writes rows-<arm>.tsv, and
prints the tables and the decision as Markdown on stdout.
"""
import json
import math
import os
import sys
from collections import Counter, defaultdict

SUPPORT, OPPOSE, ABSTAIN = "support", "oppose", "abstain"


def read_jsonl(path):
    with open(path) as f:
        return [json.loads(l) for l in f if l.strip()]


def load_arm(ev, name):
    rows = read_jsonl(os.path.join(ev, f"results-{name}.jsonl"))
    if any("not_measured" in r for r in rows):
        sys.exit(f"{name}: results marked not_measured; rule.md says KEEP")
    cals = {}
    for c in read_jsonl(os.path.join(ev, f"cal-{name}.jsonl")):
        item = os.path.basename(os.path.dirname(c["dir"]))
        cals[item] = c
    out = {}
    for r in rows:
        if r["arm"] != "swarm":
            continue
        c = cals.get(r["item"])
        if c is None:
            sys.exit(f"{name}: no calibration line for {r['item']}")
        out[r["item"]] = (r, c)
    return out


def compact(c):
    """The tally without names: each verdict with its count, most votes first."""
    counts = Counter(v for v in c["verdicts"].values() if v)
    parts = [f"{v} {n}" for v, n in sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))]
    missing = sorted(k for k, v in c["verdicts"].items() if not v)
    text = ", ".join(parts) or "no verdict"
    if missing:
        text += "; no verdict from " + ", ".join(missing)
    return text


def band(cal):
    if cal["plurality"]:
        return ">=4" if cal["margin"] >= 4 else "1-3"
    return "no vote" if cal["votes"] == 0 else "tie"


def rate(correct, n):
    return f"{correct}/{n}" + (f" ({correct / n:.3f})" if n else "")


def class_balanced(arm):
    acc = []
    for cls in (SUPPORT, OPPOSE):
        xs = [r for r, _ in arm.values() if r["want"] == cls]
        acc.append(sum(r["correct"] for r in xs) / len(xs))
    return sum(acc) / len(acc)


def sign_test(wins, losses):
    n = wins + losses
    if n == 0:
        return 1.0
    return sum(math.comb(n, k) for k in range(wins, n + 1)) / 2 ** n


def write_rows(ev, name, arm):
    cols = ["item", "want", "got", "correct", "completed", "convergence", "tally", "plurality",
            "margin", "dissent_written", "nabla_fired", "direct", "wall_seconds"]
    with open(os.path.join(ev, f"rows-{name}.tsv"), "w") as f:
        f.write("\t".join(cols) + "\n")
        for item in sorted(arm, key=item_key):
            r, c = arm[item]
            cal = c["calibration"]
            f.write("\t".join(str(x) for x in [
                item, r["want"], r.get("got", ""), r["correct"], r["completed"],
                cal["convergence"] or "none", compact(c), cal["plurality"] or "none", cal["margin"],
                cal["dissent_written"], "; ".join(cal["nabla_fired"]) or "none",
                c["verdicts"].get("direct", "-"), r["wall_seconds"]]) + "\n")


def item_key(item):
    fam, seed, var = item.split("-")
    return (int(seed[1:]), fam, var)


def table(arms):
    """Calibration table: correct/n by convergence, margin band and dissent, per arm."""
    lines = ["| cell | " + " | ".join(arms) + " |", "|---|" + "---|" * len(arms)]
    groups = [("convergence", lambda c: c["convergence"] or "none", ["high", "medium", "low", "none"]),
              ("margin", band, [">=4", "1-3", "tie", "no vote"]),
              ("dissent", lambda c: "written" if c["dissent_written"] else "not written", ["written", "not written"])]
    cells = {}
    for name, arm in arms.items():
        for r, c in arm.values():
            cal = c["calibration"]
            for g, fn, _ in groups:
                k = (g, fn(cal))
                n, ok = cells.get((name, k), (0, 0))
                cells[(name, k)] = (n + 1, ok + int(r["correct"]))
    for g, _, keys in groups:
        for k in keys:
            vals = []
            for name in arms:
                n, ok = cells.get((name, (g, k)), (0, 0))
                vals.append(rate(ok, n))
            lines.append(f"| {g} {k} | " + " | ".join(vals) + " |")
    return "\n".join(lines), cells


def main():
    ev, base_name, cand_name = sys.argv[1:4]
    base, cand = load_arm(ev, base_name), load_arm(ev, cand_name)
    excluded = sorted(i for i in base if i not in cand or base[i][0]["item_hash"] != cand[i][0]["item_hash"])
    for i in excluded:
        base.pop(i, None)
        cand.pop(i, None)
    arms = {base_name: base, cand_name: cand}
    for name, arm in arms.items():
        write_rows(ev, name, arm)

    out = []
    out.append(f"Items joined: {len(base)}" + (f"; excluded (hash differs or missing): {', '.join(excluded)}" if excluded else "; every item_hash equal across arms"))

    out.append("\n## Per arm\n")
    out.append("| metric | " + " | ".join(arms) + " |")
    out.append("|---|" + "---|" * len(arms))
    stats = {}
    for name, arm in arms.items():
        rs = [r for r, _ in arm.values()]
        s = {
            "accuracy": (sum(r["correct"] for r in rs), len(rs)),
            "cba": class_balanced(arm),
            "completed": (sum(r["completed"] for r in rs), len(rs)),
            "fabricated": sum(1 for r in rs if r["want"] == ABSTAIN and r.get("got") in (SUPPORT, OPPOSE)),
            "abstain_items": sum(1 for r in rs if r["want"] == ABSTAIN),
            "wall": sum(r["wall_seconds"] for r in rs) / len(rs),
            "tokens_in": sum(r["tokens_in"] for r in rs) / len(rs),
            "tokens_out": sum(r["tokens_out"] for r in rs) / len(rs),
        }
        direct = [(r, c) for r, c in arm.values() if "direct" in c["verdicts"]]
        if direct:
            s["direct"] = (sum(1 for r, c in direct if c["verdicts"]["direct"] == r["want"]), len(direct))
        nabla = sum(1 for _, c in arm.values() if c["calibration"]["nabla_fired"])
        s["nabla_runs"] = (nabla, len(arm))
        stats[name] = s
    def row(label, fn):
        out.append(f"| {label} | " + " | ".join(fn(stats[n]) for n in arms) + " |")
    row("accuracy", lambda s: rate(*s["accuracy"]))
    row("class-balanced accuracy", lambda s: f"{s['cba']:.3f}")
    row("completed", lambda s: rate(*s["completed"]))
    row("fabricated abstains", lambda s: f"{s['fabricated']}/{s['abstain_items']}")
    row("direct vote right", lambda s: rate(*s["direct"]) if "direct" in s else "-")
    row("runs with a ∇ pair fired", lambda s: rate(*s["nabla_runs"]))
    row("mean wall s", lambda s: f"{s['wall']:.0f}")
    row("mean tokens in / out", lambda s: f"{s['tokens_in']:.0f} / {s['tokens_out']:.0f}")

    out.append("\n## Paired by item\n")
    wins = sorted(i for i in base if cand[i][0]["correct"] and not base[i][0]["correct"])
    losses = sorted(i for i in base if base[i][0]["correct"] and not cand[i][0]["correct"])
    both_wrong = sorted(i for i in base if not base[i][0]["correct"] and not cand[i][0]["correct"])
    p = sign_test(len(wins), len(losses))
    out.append(f"wins {len(wins)}: {', '.join(wins) or '-'}")
    out.append(f"losses {len(losses)}: {', '.join(losses) or '-'}")
    out.append(f"both wrong {len(both_wrong)}: {', '.join(both_wrong) or '-'}")
    out.append(f"one-sided exact sign test p = {p:.4f} (n = {len(wins) + len(losses)})")
    out.append("\nDiscordant items:\n")
    out.append(f"| item | want | {base_name} got (conv, tally) | {cand_name} got (conv, tally) | direct |")
    out.append("|---|---|---|---|---|")
    for i in sorted(wins + losses, key=item_key):
        rb, cb = base[i]
        rc, cc = cand[i]
        out.append(f"| {i} | {rb['want']} | {rb.get('got') or '-'} ({cb['calibration']['convergence'] or 'none'}; {compact(cb)}) | {rc.get('got') or '-'} ({cc['calibration']['convergence'] or 'none'}; {compact(cc)}) | {cc['verdicts'].get('direct', '-')} |")

    out.append("\n## Calibration\n")
    t, cells = table(arms)
    out.append(t)

    out.append("\n## The rule applied\n")
    hb = cells.get((base_name, ("convergence", "high")), (0, 0))
    hc = cells.get((cand_name, ("convergence", "high")), (0, 0))
    c1 = stats[cand_name]["cba"] >= stats[base_name]["cba"]
    c2 = len(losses) <= len(wins) and p <= 0.10
    c3 = hc[0] > 0 and hb[0] > 0 and hc[1] / hc[0] >= hb[1] / hb[0] or (hc[0] > 0 and hb[0] == 0)
    c4 = stats[cand_name]["fabricated"] <= stats[base_name]["fabricated"]
    mark = lambda b: "pass" if b else "fail"
    out.append(f"1. class-balanced accuracy {stats[cand_name]['cba']:.3f} vs {stats[base_name]['cba']:.3f}: {mark(c1)}")
    out.append(f"2. losses {len(losses)} ≤ wins {len(wins)} and p = {p:.4f} ≤ 0.10: {mark(c2)} (non-inferiority reading, losses ≤ wins alone: {mark(len(losses) <= len(wins))})")
    out.append(f"3. high-convergence correct rate {rate(hc[1], hc[0])} vs {rate(hb[1], hb[0])}: {mark(c3)}")
    out.append(f"4. fabricated abstains {stats[cand_name]['fabricated']} ≤ {stats[base_name]['fabricated']}: {mark(c4)}")
    out.append(f"\n**Decision: {'ADOPT' if c1 and c2 and c3 and c4 else 'KEEP'}**")
    print("\n".join(out))


if __name__ == "__main__":
    main()
