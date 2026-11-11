"""Distill headline scoring into LFM3-1.7B with LoRA SFT (desktop GPU), then evaluate.

Teacher target per headline (our Signal schema):
  event_type, assets, confidence, novelty, horizon  <- Jan-v1-4B, prompt v2 (audited by Haiku 5.5)
  direction, magnitude                              <- mean of Jan v2 and CryptoNewsImpact when both exist
Split: katelabs = test (never trained on); other rows by time (train < 2024-07-01 <= val).
Evaluation compares the student with the teacher on val/test, and with katelabs scores and BTC
forward returns on the holdout, next to the teacher's own numbers on the same rows.

Run (venv with torch, transformers>=5.12, peft, pandas, pyarrow):
  python -I train_lfm3.py --dir ~/piquant-collect --base ~/models/LFM3-1.7B --out ~/models/piquant-news-lfm3-v1
"""
import argparse
import glob
import gzip
import hashlib
import json
import math
import os
import random
import time

import numpy as np
import pandas as pd
import torch
from peft import LoraConfig, PeftModel, get_peft_model
from transformers import AutoModelForCausalLM, AutoTokenizer

SYSTEM = ("Score this crypto/macro headline for a BTC/ETH trading model. Reply with JSON only: "
          "event_type, assets, direction (-1..1), magnitude (0..1), confidence (0..1), novelty (0..1), horizon.")
FIELDS = ["event_type", "assets", "direction", "magnitude", "confidence", "novelty", "horizon"]


def read_jsonl_gz(paths):
    rows = []
    for p in paths:
        try:
            with gzip.open(p, "rt") as f:
                rows += [json.loads(line) for line in f if line.strip()]
        except (EOFError, OSError, json.JSONDecodeError):
            pass
    return rows


def build_examples(d):
    lab = pd.read_parquet(os.path.join(d, "train", "labels.parquet"))
    lab["id"] = [hashlib.sha1(k.encode()).hexdigest()[:20] for k in lab["title_key"]]
    t = pd.DataFrame(read_jsonl_gz(glob.glob(os.path.join(d, "teacher", "*.jsonl.gz"))))
    t = t[t["parse_err"].fillna("") == ""] if "parse_err" in t else t
    t = t.drop_duplicates("id")
    sig = pd.json_normalize(t["signal"]).add_prefix("jan_")
    t = pd.concat([t[["id", "title"]].reset_index(drop=True), sig], axis=1)
    keep = ["id", "ts", "source", "split", "teacher_direction", "teacher_magnitude", "katelabs_score",
            "ret_15m_bps", "ret_60m_bps", "ret_240m_bps"]
    ex = t.merge(lab[keep], on="id", how="inner")
    cni = ex["teacher_direction"].notna()
    ex["direction"] = np.where(cni, (ex["jan_direction"] + ex["teacher_direction"]) / 2, ex["jan_direction"])
    ex["magnitude"] = np.where(cni, (ex["jan_magnitude"] + ex["teacher_magnitude"]) / 2, ex["jan_magnitude"])
    for f in ("event_type", "assets", "confidence", "novelty", "horizon"):
        ex[f] = ex["jan_" + f]
    return ex


def target_json(r):
    return json.dumps({
        "event_type": r["event_type"], "assets": list(r["assets"]) if r["assets"] is not None else [],
        "direction": round(float(r["direction"]), 2), "magnitude": round(float(r["magnitude"]), 2),
        "confidence": round(float(r["confidence"]), 2), "novelty": round(float(r["novelty"]), 2),
        "horizon": r["horizon"]}, separators=(",", ":"))


def prompt_ids(tok, title):
    msgs = [{"role": "system", "content": SYSTEM}, {"role": "user", "content": title}]
    ids = tok.apply_chat_template(msgs, add_generation_prompt=True, enable_thinking=False, tokenize=True)
    if hasattr(ids, "keys"):  # BatchEncoding in newer transformers
        ids = ids["input_ids"]
    return list(ids)


def encode(tok, title, answer, max_len):
    p = prompt_ids(tok, title)
    a = tok(answer + tok.eos_token, add_special_tokens=False)["input_ids"]
    ids = (p + a)[:max_len]
    labels = ([-100] * len(p) + a)[:max_len]  # loss on the answer only
    return ids, labels


def train(args, tok, ex):
    tr = ex[ex["split"] == "train"]
    rows = [encode(tok, r["title"], target_json(r), args.max_len) for _, r in tr.iterrows()]
    print(f"train examples={len(rows)}  mean tokens={np.mean([len(i) for i, _ in rows]):.0f}")
    model = AutoModelForCausalLM.from_pretrained(args.base, dtype=torch.bfloat16, trust_remote_code=True,
                                                 device_map="cuda")
    model.gradient_checkpointing_enable()
    model.enable_input_require_grads()
    targets = sorted({n.split(".")[-1] for n, m in model.named_modules()
                      if isinstance(m, torch.nn.Linear) and not n.endswith("lm_head")})
    model = get_peft_model(model, LoraConfig(r=args.rank, lora_alpha=2 * args.rank, lora_dropout=0.05,
                                             target_modules=targets, task_type="CAUSAL_LM"))
    model.print_trainable_parameters()
    opt = torch.optim.AdamW([p for p in model.parameters() if p.requires_grad], lr=args.lr, weight_decay=0.0)
    steps = math.ceil(len(rows) / args.batch) * args.epochs
    sched = torch.optim.lr_scheduler.LambdaLR(
        opt, lambda s: min(1.0, (s + 1) / 30) * max(0.05, 0.5 * (1 + math.cos(math.pi * s / steps))))
    pad = tok.pad_token_id
    model.train()
    step, t0 = 0, time.time()
    for ep in range(args.epochs):
        random.Random(ep).shuffle(rows)
        for b in range(0, len(rows), args.batch):
            chunk = rows[b:b + args.batch]
            L = max(len(i) for i, _ in chunk)
            ids = torch.tensor([i + [pad] * (L - len(i)) for i, _ in chunk], device="cuda")
            lab = torch.tensor([l + [-100] * (L - len(l)) for _, l in chunk], device="cuda")
            att = (torch.arange(L, device="cuda")[None, :] < torch.tensor([len(i) for i, _ in chunk], device="cuda")[:, None]).long()
            loss = model(input_ids=ids, attention_mask=att, labels=lab).loss
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            opt.step(); sched.step(); opt.zero_grad(set_to_none=True)
            step += 1
            if step % 25 == 0 or step == steps:
                print(f"epoch {ep + 1} step {step}/{steps} loss {loss.item():.4f} lr {sched.get_last_lr()[0]:.2e} "
                      f"{time.time() - t0:.0f}s", flush=True)
    os.makedirs(args.out, exist_ok=True)
    model.save_pretrained(os.path.join(args.out, "adapter"))
    return model


@torch.no_grad()
def predict(model, tok, titles, batch=32):
    tok.padding_side = "left"
    out = []
    model.eval()
    for b in range(0, len(titles), batch):
        prompts = [tok.decode(prompt_ids(tok, t)) for t in titles[b:b + batch]]
        enc = tok(prompts, return_tensors="pt", padding=True, add_special_tokens=False).to("cuda")
        gen = model.generate(**enc, max_new_tokens=96, do_sample=False, pad_token_id=tok.pad_token_id)
        for g in gen[:, enc["input_ids"].shape[1]:]:
            txt = tok.decode(g, skip_special_tokens=True).strip()
            try:
                out.append(json.loads(txt[txt.index("{"):txt.rindex("}") + 1]))
            except (ValueError, json.JSONDecodeError):
                out.append(None)
    return out


def rank_corr(a, b):
    m = a.notna() & b.notna()
    return float(a[m].rank().corr(b[m].rank())) if m.sum() > 30 else float("nan")


def evaluate(model, tok, ex, n_eval, out_dir):
    report = {}
    for split in ("val", "test"):
        s = ex[ex["split"] == split]
        s = s.sample(min(n_eval, len(s)), random_state=0) if len(s) else s
        if s.empty:
            continue
        t0 = time.time()
        pred = predict(model, tok, s["title"].tolist())
        dt = (time.time() - t0) / len(s)
        ok = [p is not None and all(k in p for k in FIELDS) for p in pred]
        P = pd.DataFrame([p if o else {} for p, o in zip(pred, ok)], index=s.index)
        v = s[np.array(ok)]
        P = P[np.array(ok)]
        r = {"n": len(s), "valid_json": float(np.mean(ok)), "sec_per_headline_gpu": round(dt, 3)}
        if len(v):
            sd = pd.to_numeric(P["direction"], errors="coerce")
            sm = pd.to_numeric(P["magnitude"], errors="coerce")
            r["event_type_acc_vs_teacher"] = float((P["event_type"] == v["event_type"]).mean())
            r["direction_sign_agree_vs_teacher"] = float((np.sign(sd.round(1)) == np.sign(v["direction"].round(1))).mean())
            r["direction_mae_vs_teacher"] = float((sd - v["direction"]).abs().mean())
            r["magnitude_mae_vs_teacher"] = float((sm - v["magnitude"]).abs().mean())
            if split == "test":
                r["student_dir_vs_katelabs"] = rank_corr(sd, v["katelabs_score"])
                r["teacher_dir_vs_katelabs"] = rank_corr(v["direction"], v["katelabs_score"])
                for h in (15, 60, 240):
                    r[f"student_dir_vs_btc_{h}m"] = rank_corr(sd, v[f"ret_{h}m_bps"])
                    r[f"teacher_dir_vs_btc_{h}m"] = rank_corr(v["direction"], v[f"ret_{h}m_bps"])
                    r[f"student_mag_vs_abs_btc_{h}m"] = rank_corr(sm, v[f"ret_{h}m_bps"].abs())
                    r[f"teacher_mag_vs_abs_btc_{h}m"] = rank_corr(v["magnitude"], v[f"ret_{h}m_bps"].abs())
        report[split] = r
        print(split, json.dumps(r, indent=1))
    with open(os.path.join(out_dir, "eval.json"), "w") as f:
        json.dump(report, f, indent=1)
    return report


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default=os.path.expanduser("~/piquant-collect"))
    ap.add_argument("--base", default=os.path.expanduser("~/models/LFM3-1.7B"))
    ap.add_argument("--out", default=os.path.expanduser("~/models/piquant-news-lfm3-v1"))
    ap.add_argument("--epochs", type=int, default=2)
    ap.add_argument("--batch", type=int, default=16)
    ap.add_argument("--lr", type=float, default=2e-4)
    ap.add_argument("--rank", type=int, default=16)
    ap.add_argument("--max-len", type=int, default=192)
    ap.add_argument("--n-eval", type=int, default=600)
    ap.add_argument("--limit", type=int, default=0, help="train on at most this many examples (smoke runs)")
    ap.add_argument("--eval-only", action="store_true", help="evaluate the saved adapter")
    ap.add_argument("--merge", action="store_true", help="save merged bf16 weights for GGUF conversion")
    a = ap.parse_args()
    tok = AutoTokenizer.from_pretrained(a.base, trust_remote_code=True)
    ex = build_examples(a.dir)
    print(ex.groupby(["split", "source"]).size().to_string())
    if a.limit:
        ex = pd.concat([ex[ex["split"] == "train"].head(a.limit), ex[ex["split"] != "train"]])
    if a.eval_only:
        base = AutoModelForCausalLM.from_pretrained(a.base, dtype=torch.bfloat16, trust_remote_code=True, device_map="cuda")
        model = PeftModel.from_pretrained(base, os.path.join(a.out, "adapter"))
    else:
        model = train(a, tok, ex)
    os.makedirs(a.out, exist_ok=True)
    evaluate(model, tok, ex, a.n_eval, a.out)
    if a.merge:
        merged = model.merge_and_unload()
        merged.save_pretrained(os.path.join(a.out, "merged"))
        tok.save_pretrained(os.path.join(a.out, "merged"))
        for f in ("configuration_lfm3.py", "modeling_lfm3.py", "lfm3_conv.py", "chat_template.jinja"):
            src = os.path.join(a.base, f)
            if os.path.exists(src):
                with open(src, "rb") as i, open(os.path.join(a.out, "merged", f), "wb") as o:
                    o.write(i.read())
        print("merged weights saved to", os.path.join(a.out, "merged"))


if __name__ == "__main__":
    main()
