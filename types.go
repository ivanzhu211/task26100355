// Package planadjust 提供门店销量预测的人工调整与层级分摊能力。
// 本包以库的形式被上层补货计划服务调用，一个 Service 实例对应一个门店。
package planadjust

import "errors"

// Days 是计划期长度（天），固定为 91 天，从周一开始，分为 13 个自然周。
const Days = 91

// Weeks 是计划期内的自然周数。
const Weeks = 13

// DaysPerWeek 是每周天数。
const DaysPerWeek = 7

// Status 表示一条调整的生命周期状态。
type Status string

const (
	StatusActive     Status = "active"     // 生效
	StatusVoided     Status = "voided"     // 作废（被相交的新调整作废）
	StatusSuperseded Status = "superseded" // 被取代（被同范围的新调整取代）
	StatusConflict   Status = "conflict"   // 冲突（重算时发现无法满足）
	StatusWithdrawn  Status = "withdrawn"  // 已撤销
)

// ProductKind 是调整范围的商品维度种类。
type ProductKind int

const (
	ProductAll      ProductKind = iota // 全店
	ProductCategory                    // 某品类
	ProductSKU                         // 某 SKU
)

// TimeKind 是调整范围的时间维度种类。
type TimeKind int

const (
	TimePeriod TimeKind = iota // 整个计划期
	TimeWeek                   // 某自然周
	TimeDay                    // 某天
)

// ProductScope 描述商品维度：全部、某品类或某 SKU。
type ProductScope struct {
	Kind     ProductKind
	Category string // Kind == ProductCategory 时有效
	SKU      string // Kind == ProductSKU 时有效
}

// TimeScope 描述时间维度：整个计划期、某周或某天。
// Week 取值 1..13，Day 取值 1..91（均为 1 起）。
type TimeScope struct {
	Kind TimeKind
	Week int
	Day  int
}

// Scope 是一条调整或一次查询的范围：商品维度 × 时间维度。
type Scope struct {
	Product ProductScope
	Time    TimeScope
}

// Adjustment 是一条人工调整记录。
type Adjustment struct {
	ID        int64
	Seq       int64 // 提交顺序，重放时按此顺序应用
	Scope     Scope
	Target    int64 // 范围内合计的目标值，非负整数
	Status    Status
	CreatedAt int64 // Unix 秒
}

// SKUInfo 描述一个 SKU 及其所属品类。
type SKUInfo struct {
	SKU      string
	Category string
}

// 预定义错误。
var (
	// ErrInfeasible 表示调整无法满足，提交被拒绝，计划值与所有调整状态均不变。
	ErrInfeasible = errors.New("planadjust: adjustment infeasible")
	// ErrNotFound 表示调整或 SKU 不存在。
	ErrNotFound = errors.New("planadjust: not found")
	// ErrInvalid 表示输入参数非法（未知 SKU/品类、越界的周/天、负目标值等）。
	ErrInvalid = errors.New("planadjust: invalid argument")
	// ErrNotInitialized 表示门店尚未初始化（未设置 SKU 清单或统计预测）。
	ErrNotInitialized = errors.New("planadjust: store not initialized")
)
