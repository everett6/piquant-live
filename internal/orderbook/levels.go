package orderbook

import "piquant-live/internal/event"

// This file holds the level-array primitive the book uses for depth updates.
//
// An insert is placed at the rank its price belongs at, so the side is strictly
// ordered after every update without a sort pass. The previous in-place loop
// appended the level first and checked the order afterwards, which dropped the
// rest of the batch whenever an insert landed out of order.
//
// The batch is validated before it is applied, so an invalid level never touches
// the book. Individual updates inside a validated batch are applied one by one:
// a later update can legitimately supersede an earlier one on the same price
// (that is how venues encode a rapid re-quote), and both are still in the
// message, so nothing is silently discarded.

// applyLevels applies absolute quantity updates (qty 0 removes a level) to one
// side. It returns false, leaving L untouched, when any update names an invalid
// level (non-positive price or negative size).
//
// A level that is not present is inserted at its rank. On a side already at
// MaxLevels, an insert that ranks better than the worst level evicts that
// level; an insert that would rank below it (deeper than the configured depth)
// is ignored.
func applyLevels(L *Levels, ups []event.Level, descending bool) bool {
	for i := range ups {
		if ups[i].Price <= 0 || ups[i].Qty < 0 {
			return false
		}
	}
	for i := range ups {
		lv := &ups[i]
		idx := -1
		for j := 0; j < int(L.N); j++ {
			if L.P[j] == lv.Price {
				idx = j
				break
			}
		}
		switch {
		case lv.Qty == 0:
			if idx >= 0 {
				n := int(L.N) - 1
				copy(L.P[idx:], L.P[idx+1:])
				copy(L.Q[idx:], L.Q[idx+1:])
				L.P[n], L.Q[n] = 0, 0
				L.N = uint8(n)
			}
		case idx >= 0:
			L.Q[idx] = lv.Qty
		default:
			pos := insertPos(L, lv.Price, descending)
			if pos >= event.MaxLevels {
				continue // deeper than the configured depth
			}
			n := int(L.N)
			if n == event.MaxLevels {
				n-- // evict the worst level to make room
			}
			copy(L.P[pos+1:n+1], L.P[pos:n])
			copy(L.Q[pos+1:n+1], L.Q[pos:n])
			L.P[pos] = lv.Price
			L.Q[pos] = lv.Qty
			L.N = uint8(n + 1)
		}
	}
	return true
}

// insertPos returns the index price belongs at, 0 being the touch.
func insertPos(L *Levels, price int64, descending bool) int {
	for i := 0; i < int(L.N); i++ {
		if (descending && L.P[i] < price) || (!descending && L.P[i] > price) {
			return i
		}
	}
	return int(L.N)
}

// strictlyOrdered reports whether L is sorted without duplicate prices,
// descending for a bid side.
func strictlyOrdered(L *Levels, descending bool) bool {
	for i := 1; i < int(L.N); i++ {
		if descending {
			if L.P[i] >= L.P[i-1] {
				return false
			}
		} else if L.P[i] <= L.P[i-1] {
			return false
		}
	}
	return true
}
