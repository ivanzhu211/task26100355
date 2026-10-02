package planadjust

import (
	"cmp"
	"math/bits"
	"slices"
)

// shares 计算每格的精确份额 q_i = total*w_i/W，拆成 floor 与余数。
// 用 128 位乘除避免溢出；总量与格子值在实际业务中远小于 2^62。
func shares(total int64, weights []int64, floors, rems []int64) (leftover int64) {
	var W int64
	for _, w := range weights {
		W += w
	}
	if W == 0 {
		// 当前全为 0：平均分，余数按格子顺序逐个补齐。
		base := total / int64(len(weights))
		extra := total % int64(len(weights))
		for i := range weights {
			floors[i] = base
			if int64(i) < extra {
				floors[i]++
			}
			rems[i] = 0
		}
		return 0
	}
	for i, w := range weights {
		hi, lo := bits.Mul64(uint64(total), uint64(w))
		var q, r uint64
		if hi >= uint64(W) { // 不可能发生的饱和保护
			q, r = ^uint64(0)>>1, 0
		} else {
			q, r = bits.Div64(hi, lo, uint64(W))
		}
		floors[i] = int64(q)
		rems[i] = int64(r)
		leftover += int64(q)
	}
	return total - leftover
}

// distribute 最大余数法：leftover 个单位分给余数最大的格子（余数相同取下标小者）。
func distribute(leftover int64, rems []int64, out []int64) {
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int {
		if c := cmp.Compare(rems[b], rems[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	for k := int64(0); k < leftover; k++ {
		out[idx[k]]++
	}
}

// allocate 把总量 total 按 weights 的比例分到各格子，
// 每格得到精确份额向下或向上取整后的值（最大余数法），合计严格等于 total。
// weights 全为 0 时平均分。返回的 out 长度与 weights 相同。
func allocate(total int64, weights []int64, out []int64) []int64 {
	if out == nil || len(out) != len(weights) {
		out = make([]int64, len(weights))
	}
	if len(weights) == 0 {
		return out
	}
	rems := make([]int64, len(weights))
	leftover := shares(total, weights, out, rems)
	if leftover > 0 {
		distribute(leftover, rems, out)
	}
	return out
}

// deallocate 从格子中按当前值比例反向扣减 amount（amount >= 0），
// 每格扣减量为其精确份额向下或向上取整，合计严格等于 amount。
// 调用方保证 sum(values) >= amount，因此任何格子不会被扣成负数。
// 扣减结果直接写回 values。
func deallocate(amount int64, values []int64) {
	if amount == 0 || len(values) == 0 {
		return
	}
	floors := make([]int64, len(values))
	rems := make([]int64, len(values))
	leftover := shares(amount, values, floors, rems)
	if leftover > 0 {
		distribute(leftover, rems, floors)
	}
	for i := range values {
		values[i] -= floors[i]
	}
}
