// Package planadjust 提供门店销量预测的人工调整与层级分摊能力。
// 本包为库，由上层补货计划服务调用；数据持久化在 SQLite。
package planadjust

import "errors"

// 计划期常量：固定 91 天、13 个自然周，从周一开始。
const (
	Days        = 91
	Weeks       = 13
	DaysPerWeek = 7
)

// ProductKind 商品维度：全部、某品类、某 SKU。
type ProductKind int

const (
	ProductAll ProductKind = iota
	ProductCategory
	ProductSKU
)

// TimeKind 时间维度：整个计划期、某周、某天。
type TimeKind int

const (
	TimePeriod TimeKind = iota
	TimeWeek
	TimeDay
)

// Scope 一条调整或一次查询的范围：商品维度 × 时间维度。
type Scope struct {
	Product  ProductKind
	Category string // Product == ProductCategory 时有效
	SKU      string // Product == ProductSKU 时有效
	Time     TimeKind
	Week     int // Time == TimeWeek 时有效，1..13
	Day      int // Time == TimeDay 时有效，1..91
}

// Status 调整的生命周期状态。
type Status int

const (
	StatusActive   Status = iota // 生效
	StatusVoided                 // 作废（被相交但互不包含的新调整作废）
	StatusReplaced               // 被取代（被同范围的新调整取代）
	StatusConflict               // 冲突（重算时发现无法满足，不再生效）
	StatusUndone                 // 已撤销
)

func (s Status) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusVoided:
		return "voided"
	case StatusReplaced:
		return "replaced"
	case StatusConflict:
		return "conflict"
	case StatusUndone:
		return "undone"
	}
	return "unknown"
}

// Adjustment 一条人工调整：令 Scope 范围内计划值合计等于 Target。
type Adjustment struct {
	ID     int64
	Scope  Scope
	Target int64
	Status Status
}

// SKUDef 门店的商品档案：每个 SKU 属于一个品类。
type SKUDef struct {
	SKU      string
	Category string
}

// Config 新建门店计划时的配置。
type Config struct {
	StoreID string
	SKUs    []SKUDef
}

var (
	// ErrInfeasible 调整无法满足，提交被拒绝，计划值与所有调整状态不变。
	ErrInfeasible = errors.New("planadjust: adjustment infeasible")
	// ErrNotFound 调整不存在。
	ErrNotFound = errors.New("planadjust: adjustment not found")
	// ErrNotActive 只有生效中的调整可以撤销。
	ErrNotActive = errors.New("planadjust: adjustment not active")
	// ErrInvalidScope 范围非法（未知 SKU/品类、周/日越界等）。
	ErrInvalidScope = errors.New("planadjust: invalid scope")
	// ErrInvalidTarget 目标值为负。
	ErrInvalidTarget = errors.New("planadjust: negative target")
	// ErrExists 数据库已初始化过。
	ErrExists = errors.New("planadjust: database already initialized")
)
