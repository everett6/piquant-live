"""Simulated quantum annealing (path-integral Monte Carlo) over binary-encoded parameter spaces.

P Trotter replicas of a bitstring are coupled ferromagnetically along the imaginary-time axis with strength
J_perp = -(P*T/2) * ln(tanh(Gamma / (P*T))). Annealing the transverse field Gamma from high to low lets
replicas tunnel between configurations early and freeze into a common low-energy state late. This is a
classical algorithm inspired by quantum annealing (no quantum hardware or speed-up claimed).

The objective is cached, and every distinct configuration evaluated counts toward the trial budget, so the
search can be charged honestly in the deflated Sharpe ratio.
"""
from __future__ import annotations

import math
from typing import Callable

import numpy as np


class BudgetedObjective:
    """Wraps energy(bits) with a cache and a hard budget on distinct evaluations."""

    def __init__(self, energy: Callable[[tuple], float], budget: int):
        self.energy, self.budget, self.cache = energy, budget, {}

    def __call__(self, bits: tuple) -> float:
        if bits not in self.cache:
            if len(self.cache) >= self.budget:
                return math.inf  # out of budget: unseen configs are treated as unreachable
            self.cache[bits] = float(self.energy(bits))
        return self.cache[bits]

    @property
    def exhausted(self) -> bool:
        return len(self.cache) >= self.budget

    def best(self):
        k = min(self.cache, key=self.cache.get)
        return k, self.cache[k]


def sqa(obj: BudgetedObjective, n_bits: int, replicas: int = 8, temperature: float = 0.05,
        gamma0: float = 3.0, gamma1: float = 0.01, sweeps: int = 200, seed: int = 0):
    rng = np.random.default_rng(seed)
    state = rng.integers(0, 2, size=(replicas, n_bits))
    energies = np.array([obj(tuple(s)) for s in state])
    P, T = replicas, temperature
    for sweep in range(sweeps):
        if obj.exhausted:
            break
        gamma = gamma0 * (gamma1 / gamma0) ** (sweep / max(1, sweeps - 1))
        jp = -(P * T / 2) * math.log(math.tanh(gamma / (P * T)))
        for k in rng.permutation(P):
            for i in rng.permutation(n_bits):
                cand = state[k].copy()
                cand[i] ^= 1
                e_new = obj(tuple(cand))
                if not math.isfinite(e_new):
                    continue
                spin = lambda b: 1 if b else -1
                up, dn = state[(k - 1) % P][i], state[(k + 1) % P][i]
                # change in the replica-coupling term when flipping bit i of replica k
                d_coupling = -jp * (spin(cand[i]) - spin(state[k][i])) * (spin(up) + spin(dn))
                d_total = (e_new - energies[k]) / P + d_coupling
                if d_total <= 0 or rng.random() < math.exp(-d_total / T):
                    state[k], energies[k] = cand, e_new
    return obj.best()


def random_search(obj: BudgetedObjective, n_bits: int, seed: int = 0):
    rng = np.random.default_rng(seed)
    tries = 0
    while not obj.exhausted and tries < obj.budget * 50:
        obj(tuple(rng.integers(0, 2, size=n_bits)))
        tries += 1
    return obj.best()


def decode(bits: tuple, fields: list[tuple[str, list]]) -> dict:
    """fields: [(name, values)] with len(values) a power of two; bits consumed in order."""
    out, pos = {}, 0
    for name, values in fields:
        w = int(math.log2(len(values)))
        idx = 0
        for b in bits[pos:pos + w]:
            idx = idx * 2 + int(b)
        out[name] = values[idx]
        pos += w
    return out
