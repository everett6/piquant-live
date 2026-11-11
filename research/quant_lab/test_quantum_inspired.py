"""Self-checking tests for quantum_inspired.py (python test_quantum_inspired.py)."""
import numpy as np

import quantum_inspired as Q


def rugged(bits):
    """Deceptive landscape on 10 bits: global minimum at all-ones, a wide local basin at all-zeros."""
    x = np.array(bits)
    ones = x.sum()
    return -10.0 if ones == len(x) else (ones - 3) ** 2 * 0.3 - 2.0 * (ones <= 1)


def test_budget_enforced():
    obj = Q.BudgetedObjective(rugged, budget=20)
    Q.random_search(obj, 10, seed=1)
    assert len(obj.cache) == 20


def test_decode():
    fields = [("a", [1, 2, 3, 4]), ("b", ["x", "y"])]
    assert Q.decode((1, 0, 1), fields) == {"a": 3, "b": "y"}


def test_sqa_beats_random_on_average():
    wins = 0
    for seed in range(10):
        o1 = Q.BudgetedObjective(rugged, budget=150)
        o2 = Q.BudgetedObjective(rugged, budget=150)
        _, e_sqa = Q.sqa(o1, 10, seed=seed, temperature=0.5, sweeps=400)
        _, e_rnd = Q.random_search(o2, 10, seed=seed)
        wins += e_sqa <= e_rnd
    assert wins >= 6, wins


if __name__ == "__main__":
    for name, fn in list(globals().items()):
        if name.startswith("test_"):
            fn()
            print("ok", name)
