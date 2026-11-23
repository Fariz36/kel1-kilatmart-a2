# Failure experiments

The brief asks for **one** failure experiment: *predict the result, run it, record what happened*
(slide 8). **Write the prediction in the table before you run anything.** The slides compare
your prediction with the result.

`run.sh` does the steps and saves the full output to `results/`, so you have evidence for the slides.

| # | Experiment | Command | Prediction (write first) | Result (from the log) | Explanation |
|---|---|---|---|---|---|
| E1 | A source is down (main experiment) | `experiments/run.sh source-down` | | | |
| E2 | A source hangs (paused container) | `experiments/run.sh source-slow` | | | |
| E3 | The source schema changes | `experiments/run.sh schema-change` | | | |

## Questions worth predicting

- Does the analytics team get a **partial** answer (only orders) or **no** answer?
- How long until the error comes back? Is the error message clear about *which* system failed?
- After the source comes back, does the federation service need a restart or a resync?
- Does `FEDERATION_MODE=persistent` (attach once, reuse) behave differently from `fresh` (attach
  per request)? What about the speed of a normal query in each mode?
- For E3: who finds out first that the schema changed, the inventory team or the analytics team?
  Compare that with an ETL pipeline (A3) or CDC (A1).

## Switching mode

```bash
FEDERATION_MODE=persistent docker compose up -d federation   # attach once
docker compose up -d federation                              # back to fresh (default)
```

Reset all data afterwards with `scripts/reset.sh`.
