# Autolog for training libraries (HF Trainer / Lightning)

Experiment tracking / Priority: medium

## Current state

To record anything, users have to manually write `trackio.log(...)` calls into their
training loop.

## To do

We don't need to chase MLflow's full autolog coverage (sklearn / xgboost / keras / ...).
This product's users skew toward the HF ecosystem, so **two integrations cover most
of it**:

1. Provide a `TrainerCallback` for `transformers`
   - `init()` on `on_train_begin`, `log()` on `on_log`, `finish()` on `on_train_end`
   - Flatten `TrainingArguments` into config (under a namespace that doesn't collide
     with `_meta.*`)
   - A thin bridge equivalent to `report_to="wandb"` is enough, since trackio has a
     wandb-compatible signature
2. Provide a PyTorch Lightning `Logger` implementation

Place these under `clients/python/thinkingface/`. Treat `transformers` / `lightning` as
**optional dependencies** (if they can't be imported, simply don't expose that
callback).

```python
from thinkingface.trackio.integrations import ThinkingFaceCallback

trainer = Trainer(..., callbacks=[ThinkingFaceCallback(project="mnist")])
```

## Definition of done

- `import thinkingface.trackio` doesn't break in an environment without `transformers`
  installed
- A run gets created via the callback, and metrics plus `TrainingArguments` get
  recorded
- Usage is documented in `clients/python/README.md`
