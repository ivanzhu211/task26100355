package planadjust

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"

	_ "modernc.org/sqlite"
)

// SQLite 持久化：门店档案、统计预测、计划值快照、全部调整及状态。
// 每次状态变更在同一事务内落盘，保证进程重启后完全一致。

const schema = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS categories (
  idx     INTEGER PRIMARY KEY,
  cat_key TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS skus (
  idx     INTEGER PRIMARY KEY,
  sku_key TEXT NOT NULL UNIQUE,
  cat_idx INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grids (
  name TEXT PRIMARY KEY,
  data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS adjustments (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  pkind   INTEGER NOT NULL,
  cat_idx INTEGER NOT NULL,
  sku_idx INTEGER NOT NULL,
  tkind   INTEGER NOT NULL,
  tref    INTEGER NOT NULL,
  target  INTEGER NOT NULL,
  status  INTEGER NOT NULL
);
`

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func (p *Planner) initSchema() error {
	_, err := p.db.Exec(schema)
	return err
}

func encodeGrid(grid []int64) []byte {
	buf := make([]byte, len(grid)*8)
	for i, v := range grid {
		binary.LittleEndian.PutUint64(buf[i*8:], uint64(v))
	}
	return buf
}

func decodeGrid(buf []byte) ([]int64, error) {
	if len(buf)%8 != 0 {
		return nil, fmt.Errorf("planadjust: corrupt grid blob (%d bytes)", len(buf))
	}
	grid := make([]int64, len(buf)/8)
	for i := range grid {
		grid[i] = int64(binary.LittleEndian.Uint64(buf[i*8:]))
	}
	return grid, nil
}

func saveGrid(tx *sql.Tx, name string, grid []int64) error {
	_, err := tx.Exec(`INSERT INTO grids(name, data) VALUES(?, ?)
		ON CONFLICT(name) DO UPDATE SET data=excluded.data`, name, encodeGrid(grid))
	return err
}

func loadGrid(db *sql.DB, name string) ([]int64, error) {
	var buf []byte
	err := db.QueryRow(`SELECT data FROM grids WHERE name=?`, name).Scan(&buf)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeGrid(buf)
}

// persist 把当前全部状态（预测、计划值、调整及状态）在一个事务内落盘。
func (p *Planner) persist() error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveGrid(tx, "forecast", p.forecast); err != nil {
		return err
	}
	if err := saveGrid(tx, "plan", p.plan); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO adjustments
		(id, pkind, cat_idx, sku_idx, tkind, tref, target, status)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range p.entries {
		catIdx, skuIdx, tref := -1, -1, 0
		switch e.adj.Scope.Product {
		case ProductCategory:
			catIdx = p.catIdx[e.adj.Scope.Category]
		case ProductSKU:
			skuIdx = p.skuIdx[e.adj.Scope.SKU]
		}
		switch e.adj.Scope.Time {
		case TimeWeek:
			tref = e.adj.Scope.Week
		case TimeDay:
			tref = e.adj.Scope.Day
		}
		if _, err := stmt.Exec(e.adj.ID, e.adj.Scope.Product, catIdx, skuIdx,
			e.adj.Scope.Time, tref, e.adj.Target, e.adj.Status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// saveCatalog 新建时写入门店档案。
func (p *Planner) saveCatalog(storeID string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('store_id', ?)`, storeID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('format_version', '1')`); err != nil {
		return err
	}
	for i, c := range p.catKeys {
		if _, err := tx.Exec(`INSERT INTO categories(idx, cat_key) VALUES(?, ?)`, i, c); err != nil {
			return err
		}
	}
	for i, s := range p.skuKeys {
		if _, err := tx.Exec(`INSERT INTO skus(idx, sku_key, cat_idx) VALUES(?, ?, ?)`, i, s, p.skuCat[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// load 从数据库恢复全部状态。
func (p *Planner) load() error {
	var version string
	if err := p.db.QueryRow(`SELECT value FROM meta WHERE key='format_version'`).Scan(&version); err != nil {
		return fmt.Errorf("planadjust: not an initialized database: %w", err)
	}

	rows, err := p.db.Query(`SELECT idx, cat_key FROM categories ORDER BY idx`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var idx int
		var key string
		if err := rows.Scan(&idx, &key); err != nil {
			rows.Close()
			return err
		}
		p.catKeys = append(p.catKeys, key)
		p.catIdx[key] = idx
	}
	rows.Close()

	rows, err = p.db.Query(`SELECT idx, sku_key, cat_idx FROM skus ORDER BY idx`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var idx, cat int
		var key string
		if err := rows.Scan(&idx, &key, &cat); err != nil {
			rows.Close()
			return err
		}
		p.skuKeys = append(p.skuKeys, key)
		p.skuIdx[key] = idx
		p.skuCat = append(p.skuCat, cat)
	}
	rows.Close()

	p.catSKUs = make([][]int, len(p.catKeys))
	for k, c := range p.skuCat {
		p.catSKUs[c] = append(p.catSKUs[c], k)
	}

	n := len(p.skuKeys) * Days
	p.forecast, err = loadGrid(p.db, "forecast")
	if err != nil {
		return err
	}
	if p.forecast == nil {
		p.forecast = make([]int64, n)
	}
	if len(p.forecast) != n {
		return fmt.Errorf("planadjust: forecast size mismatch")
	}
	p.plan, err = loadGrid(p.db, "plan")
	if err != nil {
		return err
	}
	if p.plan == nil {
		p.plan = make([]int64, n)
	}
	if len(p.plan) != n {
		return fmt.Errorf("planadjust: plan size mismatch")
	}

	rows, err = p.db.Query(`SELECT id, pkind, cat_idx, sku_idx, tkind, tref, target, status
		FROM adjustments ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e adjEntry
		var pkind, catIdx, skuIdx, tkind, tref, status int
		var target int64
		if err := rows.Scan(&e.adj.ID, &pkind, &catIdx, &skuIdx, &tkind, &tref, &target, &status); err != nil {
			return err
		}
		e.adj.Target = target
		e.adj.Status = Status(status)
		sc := Scope{Product: ProductKind(pkind), Time: TimeKind(tkind)}
		switch sc.Product {
		case ProductCategory:
			sc.Category = p.catKeys[catIdx]
		case ProductSKU:
			sc.SKU = p.skuKeys[skuIdx]
		}
		switch sc.Time {
		case TimeWeek:
			sc.Week = tref
		case TimeDay:
			sc.Day = tref
		}
		e.adj.Scope = sc
		isc, err := p.resolve(sc)
		if err != nil {
			return err
		}
		e.sc = isc
		p.entries = append(p.entries, &e)
	}
	return rows.Err()
}

// nextID 下一条调整的 ID（提交顺序号）。
func (p *Planner) nextID() int64 {
	if len(p.entries) == 0 {
		return 1
	}
	return p.entries[len(p.entries)-1].adj.ID + 1
}

var _ = math.MaxInt64 // 保留：目标值上限约束在文档中说明
