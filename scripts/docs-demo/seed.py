#!/usr/bin/env python3
"""Seed a throwaway instance with the demo content used by the docs screenshots.

The images under `docs/users/images/` must not show whatever happens to be in a
developer's compose stack, so they are captured from a dedicated instance that
holds nothing but the content created here. Run this first, then
`scripts/docs-demo/shots.py`.

Prerequisites (see docs/dev/docs-screenshots.md):

    docker run -d --name tf-docs-gcs -p 4499:4443 \
        fsouza/fake-gcs-server:1.55.1@sha256:91afded49de804aa61b5f3eb6c7cd65205acf9e5c5e047cf0ba7d9507af806c8 \
        -scheme=http -public-host=localhost:4499 -port=4443 -filesystem-root=/data
    API_DEV_PORT=8091 GCS_PROXY_PORT=4499 DEV_DIR=.dev/docs-demo make dev-api
    make dev-web WEB_DEV_PORT=3120 NEXT_PUBLIC_API_URL=http://localhost:8091 \
        API_URL=http://localhost:8091

Usage (versions pinned like every other install step -- see docs/dev/supply-chain.md):

    uv run --isolated --with huggingface_hub==1.28.0 --with pandas==2.3.3 --with pyarrow==25.0.1 \
        --with requests==2.34.2 --with ./clients/python scripts/docs-demo/seed.py
"""

from __future__ import annotations

import io
import json
import math
import os
import random
import struct
import time

import pandas as pd
import requests

ENDPOINT = os.environ.get("DOCS_DEMO_ENDPOINT", "http://localhost:8091")
TIMEOUT = 15

POSITIVE = [
    "Beautifully shot, and it held my attention right to the last frame.",
    "The lead performance is superb; I have already watched it twice.",
    "A careful script that lets every character earn their ending.",
    "The score and the cinematography carry each other completely.",
    "Far better than I expected, and I have been recommending it all week.",
    "Quietly confident film-making, with not a wasted scene in it.",
    "Funny without ever being cruel, which is harder than it looks.",
    "The third act lands because the first two do the work properly.",
    "An unshowy film that trusts the audience to keep up.",
    "Gorgeous production design, and a cast that clearly enjoyed itself.",
    "I went in sceptical about the remake and came out convinced.",
    "The dialogue crackles; whole scenes play on rhythm alone.",
]
NEGATIVE = [
    "The pacing drags so badly that I lost interest halfway through.",
    "The premise never holds together, so none of it lands.",
    "Characters act against their own logic whenever the plot needs it.",
    "The trailer was more entertaining than the finished film.",
    "Long, thin, and oddly pleased with itself.",
    "Competent but forgettable; nothing here you have not seen before.",
    "Every emotional beat is announced twenty minutes before it arrives.",
    "Beautiful to look at and completely hollow underneath.",
    "The editing is so choppy that the action becomes unreadable.",
    "A promising setup abandoned for a generic chase in the last half hour.",
    "Two good performances stranded in a script that fails them.",
    "It mistakes volume for tension and length for depth.",
]


def reviews(rows: int, seed: int) -> pd.DataFrame:
    rng = random.Random(seed)
    out = []
    for i in range(rows):
        positive = rng.random() < 0.5
        text = rng.choice(POSITIVE if positive else NEGATIVE)
        out.append(
            {
                "id": i,
                "text": text,
                "label": 1 if positive else 0,
                "label_text": "positive" if positive else "negative",
                "rating": rng.randint(7, 10) if positive else rng.randint(1, 4),
                "num_chars": len(text),
            }
        )
    return pd.DataFrame(out)


def parquet(df: pd.DataFrame) -> bytes:
    buf = io.BytesIO()
    df.to_parquet(buf, index=False)
    return buf.getvalue()


def safetensors(tensors: dict[str, tuple[str, list[int]]]) -> bytes:
    """A valid safetensors file: 8-byte header length, JSON header, then zeros.

    The metadata viewer only ever reads the header, so the payload does not have
    to hold real weights -- but it does have to be the right size, which is what
    makes the model page report a realistic parameter count and file size.
    """
    itemsize = {"F32": 4, "F16": 2, "I64": 8}
    header: dict[str, object] = {}
    offset = 0
    for name, (dtype, shape) in tensors.items():
        count = math.prod(shape)
        nbytes = count * itemsize[dtype]
        header[name] = {
            "dtype": dtype,
            "shape": shape,
            "data_offsets": [offset, offset + nbytes],
        }
        offset += nbytes
    header["__metadata__"] = {"format": "pt"}
    blob = json.dumps(header).encode()
    blob += b" " * ((8 - len(blob) % 8) % 8)
    return struct.pack("<Q", len(blob)) + blob + b"\0" * offset


def bert_tensors(hidden: int = 768, layers: int = 12, vocab: int = 32000):
    tensors = {"embeddings.word_embeddings.weight": ("F32", [vocab, hidden])}
    for i in range(layers):
        for part in ("query", "key", "value"):
            tensors[f"encoder.layer.{i}.attention.self.{part}.weight"] = (
                "F32",
                [hidden, hidden],
            )
    tensors["classifier.weight"] = ("F32", [2, hidden])
    tensors["classifier.bias"] = ("F32", [2])
    return tensors


CONFIG_JSON = json.dumps(
    {
        "architectures": ["BertForSequenceClassification"],
        "model_type": "bert",
        "hidden_size": 768,
        "num_hidden_layers": 12,
        "num_attention_heads": 12,
        "vocab_size": 32000,
        "id2label": {"0": "negative", "1": "positive"},
        "label2id": {"negative": 0, "positive": 1},
        "torch_dtype": "float32",
    },
    indent=2,
).encode()

DATASET_CARD = """---
license: cc-by-4.0
language:
  - en
task_categories:
  - text-classification
tags:
  - sentiment
  - reviews
size_categories:
  - 1K<n<10K
---

# imdb-reviews

Movie reviews labelled for binary sentiment. Each row carries the raw review text, a
numeric `label` (`1` positive / `0` negative) and its readable form.

## Splits

| Split | Rows |
|---|---|
| train | 800 |
| test | 200 |

## Usage

```python
from datasets import load_dataset

ds = load_dataset("admin/imdb-reviews")
```

## Fields

- `id` -- row identifier
- `text` -- the review body
- `label` -- `1` for positive, `0` for negative
- `label_text` -- the label spelled out
- `rating` -- the reviewer's score out of 10
- `num_chars` -- character count of `text`
"""

MODEL_CARD = """---
license: apache-2.0
language:
  - en
library_name: transformers
pipeline_tag: text-classification
base_model: admin/bert-base-en
tags:
  - sentiment
  - bert
datasets:
  - admin/imdb-reviews
metrics:
  - accuracy
---

# sentiment-base

A BERT-base classifier fine-tuned on `admin/imdb-reviews` for binary sentiment.

## Results

| Split | Accuracy | F1 |
|---|---|---|
| validation | 0.931 | 0.929 |
| test | 0.924 | 0.921 |

## Usage

```python
from transformers import pipeline

clf = pipeline("text-classification", model="acme/sentiment-base")
clf("Beautifully shot, and it held my attention right to the last frame.")
```

## Training

Fine-tuned for 3 epochs at a learning rate of 3e-5 with a batch size of 32. The full
metric history is recorded in the `sentiment-finetune` experiment project.
"""


def card(name: str, body: str, **front: str) -> bytes:
    lines = ["---"]
    for key, value in front.items():
        lines.append(f"{key.replace('_', '-')}: {value}")
    lines += ["---", "", f"# {name}", "", body, ""]
    return "\n".join(lines).encode()


def main() -> None:
    session = requests.Session()
    session.post(
        f"{ENDPOINT}/api/v1/auth/login",
        json={"username": "admin", "password": "admin"},
        timeout=TIMEOUT,
    ).raise_for_status()
    token = session.post(
        f"{ENDPOINT}/api/v1/tokens",
        json={"name": "seeding", "scope": "write"},
        timeout=TIMEOUT,
    ).json()["token"]

    os.environ.update(HF_ENDPOINT=ENDPOINT, HF_TOKEN=token, HF_HUB_DISABLE_XET="1")
    from huggingface_hub import HfApi

    api = HfApi(endpoint=ENDPOINT, token=token)

    session.post(
        f"{ENDPOINT}/api/v1/orgs",
        json={"name": "acme", "display_name": "Acme Research"},
        timeout=TIMEOUT,
    )

    for username, role in [("alice", "admin"), ("bob", "write"), ("carol", "read")]:
        requests.post(
            f"{ENDPOINT}/api/v1/auth/signup",
            json={
                "username": username,
                "password": "demo-password-1",
                "email": f"{username}@example.com",
            },
            timeout=TIMEOUT,
        )
        session.post(
            f"{ENDPOINT}/api/v1/orgs/acme/members",
            json={"username": username, "role": role},
            timeout=TIMEOUT,
        )

    def upload(repo_id: str, kind: str, files: dict[str, bytes]) -> None:
        api.create_repo(repo_id, repo_type=kind, exist_ok=True)
        for path, data in files.items():
            api.upload_file(
                path_or_fileobj=data, path_in_repo=path, repo_id=repo_id, repo_type=kind
            )
        print(f"  {kind} {repo_id}")

    upload(
        "admin/imdb-reviews",
        "dataset",
        {
            "README.md": DATASET_CARD.encode(),
            "data/train.parquet": parquet(reviews(800, 11)),
            "data/test.parquet": parquet(reviews(200, 12)),
        },
    )
    upload(
        "acme/ag-news",
        "dataset",
        {
            "README.md": card(
                "ag-news",
                "News articles grouped by section, for topic classification benchmarks.",
                license="cc-by-sa-3.0",
                tags="[news]",
            ),
            "data/train.parquet": parquet(reviews(400, 13)),
        },
    )
    upload(
        "admin/wiki-summary",
        "dataset",
        {
            "README.md": card(
                "wiki-summary",
                "Article and summary pairs extracted from Wikipedia.",
                license="cc-by-sa-4.0",
                tags="[summarization]",
            ),
            "data/train.parquet": parquet(reviews(300, 14)),
        },
    )
    upload(
        "acme/training-runs",
        "dataset",
        {
            "README.md": card(
                "training-runs",
                "Experiment tracking repository for Acme Research. Metric series logged from "
                "training scripts land here as Parquet and are indexed into projects and runs.",
                license="apache-2.0",
                tags="[experiments]",
            )
        },
    )

    weights = safetensors(bert_tensors())
    upload(
        "acme/sentiment-base",
        "model",
        {
            "README.md": MODEL_CARD.encode(),
            "config.json": CONFIG_JSON,
            "model.safetensors": weights,
        },
    )
    upload(
        "admin/bert-base-en",
        "model",
        {
            "README.md": card(
                "bert-base-en",
                "A BERT-base checkpoint pretrained on Wikipedia and BookCorpus, intended as a "
                "starting point for fine-tuning.",
                license="apache-2.0",
                tags="[bert, pretrained]",
            ),
            "config.json": CONFIG_JSON,
            "model.safetensors": weights,
        },
    )
    upload(
        "acme/ner-conll",
        "model",
        {
            "README.md": card(
                "ner-conll",
                "Named-entity recognition over the CoNLL-2003 PER / ORG / LOC / MISC label set.",
                license="mit",
                tags="[ner]",
            ),
            "config.json": CONFIG_JSON,
        },
    )

    os.environ.update(
        THINKINGFACE_ENDPOINT=ENDPOINT,
        THINKINGFACE_TOKEN=token,
        THINKINGFACE_REPO="acme/training-runs",
    )
    from thinkingface import trackio

    runs = [
        ("baseline", 2e-5, 16, 0.905, 0.62),
        ("lr-3e-5", 3e-5, 32, 0.931, 0.58),
        ("lr-5e-5", 5e-5, 32, 0.918, 0.66),
        ("bs-64", 3e-5, 64, 0.897, 0.70),
    ]
    for i, (name, lr, batch, best, start) in enumerate(runs):
        rng = random.Random(300 + i)
        trackio.init(
            project="sentiment-finetune",
            name=name,
            config={
                "learning_rate": lr,
                "batch_size": batch,
                "epochs": 3,
                "model": "bert-base-uncased",
                "dataset": "admin/imdb-reviews",
                "seed": 42 + i,
            },
        )
        for step in range(60):
            p = step / 59
            trackio.log(
                {
                    "train/loss": round(
                        start * math.exp(-2.6 * p) + rng.uniform(-0.012, 0.012) + 0.04,
                        4,
                    ),
                    "eval/loss": round(
                        start * math.exp(-2.2 * p) + rng.uniform(-0.010, 0.010) + 0.07,
                        4,
                    ),
                    "eval/accuracy": round(
                        min(
                            best
                            - (best - 0.52) * math.exp(-3.4 * p)
                            + rng.uniform(-0.006, 0.006),
                            0.999,
                        ),
                        4,
                    ),
                },
                step=step,
            )
        if name == "lr-3e-5":
            trackio.log_model("acme/sentiment-base")
        trackio.finish()
        print(f"  run {name}")

    # The tokens page is one of the screenshots, so leave a believable list
    # behind instead of the ones this script minted for itself.
    for existing in session.get(f"{ENDPOINT}/api/v1/tokens", timeout=TIMEOUT).json()[
        "items"
    ]:
        session.delete(f"{ENDPOINT}/api/v1/tokens/{existing['id']}", timeout=TIMEOUT)
    for name, scope in [
        ("ci-readonly", "read"),
        ("training-cluster", "write"),
        ("laptop", "write"),
    ]:
        session.post(
            f"{ENDPOINT}/api/v1/tokens",
            json={"name": name, "scope": scope},
            timeout=TIMEOUT,
        )
        time.sleep(1.1)  # so the list has a stable, non-identical ordering

    print("seeded")


if __name__ == "__main__":
    main()
