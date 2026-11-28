## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- When graphify-out/graph.json exists, run `graphify query "<question>"` before your first grep, search or source-file read for a codebase question, and put this rule in every subagent prompt that explores code. Read raw files only for lines the graph or an earlier read already located. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
- A committed graphify-out/graph.json is rewritten whole on every build, so a large diff to it in a pull request is expected. Do not flag it as churn or suggest skipping the graph rebuild or its hooks.

## Pre-registration numbering (research/quant_lab)

- Never reuse or overwrite an existing `PREREGnn.md`, `strategiesNN.py`, `run_roundNN.py`, `test_strategiesNN.py` or
  `results/roundNN.json`. Before starting a test, run `ls research/quant_lab/PREREG*.md` and take the next number above
  the highest one present. PREREG45 and PREREG46 were overwritten this way on 2026-11-18; the originals were restored
  as `PREREG45s*` and `PREREG46s*`.
- Read the trial counter in README.md immediately before committing and add your trials to that value.
