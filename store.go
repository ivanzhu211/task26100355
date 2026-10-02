package planadjust

import (
	"database/sql"
	"encoding/binary"
	"fmt"

	_ "modernc.org/sqlite"
)

// store 负责 SQLite 持久化：门店元信息、SKU 清单、统计预测与全部调整记录。
// 计划值不落盘，打开时由统计预测与生效调整确定性重放恢复。
type store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skus (
  idx      INTEGER PRIMARY KEY,
  sku      TEXT NOT NULL UNIQUE,
  category TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS forecast (
  sku_idx INTEGER PRIMARY KEY,
  data    BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS adjustments (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  seq        INTEGER NOT NULL UNIQUE,
  prod_kind  INTEGER NOT NULL,
  category   TEXT NOT NULL DEFAULT '',
  sku        TEXT NOT NULL DEFAULT '',
  time_kind  INTEGER NOT NULL,
  week       INTEGER NOT NULL DEFAULT 0,
  day        INTEGER NOT NULL DEFAULT 0,
  target     INTEGER NOT NULL,
  status     TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`

// openStore 打开（必要时创建）数据库文件并建表。
func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

// close 关闭数据库。
func (s *store) close() error {
	return s.db.Close()
}

// loadMeta 读取门店元信息。不存在时返回 ok=false。
func (s *store) loadMeta() (periodStart string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT value FROM meta WHERE key='period_start'`).Scan(&periodStart)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return periodStart, true, nil
}

// initStore 写入门店元信息与 SKU 清单（仅初始化时调用一次）。
func (s *store) initStore(periodStart string, skus []SKUInfo) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('period_start',?)`, periodStart); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO skus(idx,sku,category) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, sku := range skus {
		if _, err := stmt.Exec(i, sku.SKU, sku.Category); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadSKUs 读取 SKU 清单（按行号升序）。
func (s *store) loadSKUs() ([]SKUInfo, error) {
	rows, err := s.db.Query(`SELECT sku,category FROM skus ORDER BY idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SKUInfo
	for rows.Next() {
		var info SKUInfo
		if err := rows.Scan(&info.SKU, &info.Category); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// saveForecast 整版保存统计预测（每 SKU 一行 blob，91 个 int64 小端）。
func (s *store) saveForecast(f *grid, l *layout) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM forecast`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO forecast(sku_idx,data) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	buf := make([]byte, Days*8)
	for r := 0; r < f.rows; r++ {
		for d := 0; d < Days; d++ {
			binary.LittleEndian.PutUint64(buf[d*8:], uint64(f.values[r*Days+d]))
		}
		if _, err := stmt.Exec(r, buf); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadForecast 读取统计预测；从未保存时返回全零网格。
func (s *store) loadForecast(l *layout) (*grid, error) {
	f := newGrid(l)
	rows, err := s.db.Query(`SELECT sku_idx,data FROM forecast`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var data []byte
		if err := rows.Scan(&idx, &data); err != nil {
			return nil, err
		}
		if idx < 0 || idx >= f.rows || len(data) != Days*8 {
			return nil, fmt.Errorf("planadjust: corrupt forecast row %d", idx)
		}
		for d := 0; d < Days; d++ {
			v := int64(binary.LittleEndian.Uint64(data[d*8:]))
			f.values[idx*Days+d] = v
			f.catDay[f.catOf[idx]*Days+d] += v
			f.allDay[d] += v
			f.total += v
		}
	}
	return f, rows.Err()
}

// insertAdjustment 插入一条调整并回填自增 ID。
func (s *store) insertAdjustment(adj *Adjustment) error {
	res, err := s.db.Exec(
		`INSERT INTO adjustments(seq,prod_kind,category,sku,time_kind,week,day,target,status,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		adj.Seq, int(adj.Scope.Product.Kind), adj.Scope.Product.Category, adj.Scope.Product.SKU,
		int(adj.Scope.Time.Kind), adj.Scope.Time.Week, adj.Scope.Time.Day,
		adj.Target, string(adj.Status), adj.CreatedAt,
	)
	if err != nil {
		return err
	}
	adj.ID, err = res.LastInsertId()
	return err
}

// updateStatus 更新一条调整的状态。
func (s *store) updateStatus(id int64, st Status) error {
	_, err := s.db.Exec(`UPDATE adjustments SET status=? WHERE id=?`, string(st), id)
	return err
}

// loadAdjustments 读取全部调整（按 Seq 升序）。
func (s *store) loadAdjustments() ([]*Adjustment, error) {
	rows, err := s.db.Query(
		`SELECT id,seq,prod_kind,category,sku,time_kind,week,day,target,status,created_at
		 FROM adjustments ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Adjustment
	for rows.Next() {
		var adj Adjustment
		var pk, tk int
		var st string
		if err := rows.Scan(&adj.ID, &adj.Seq, &pk, &adj.Scope.Product.Category, &adj.Scope.Product.SKU,
			&tk, &adj.Scope.Time.Week, &adj.Scope.Time.Day, &adj.Target, &st, &adj.CreatedAt); err != nil {
			return nil, err
		}
		adj.Scope.Product.Kind = ProductKind(pk)
		adj.Scope.Time.Kind = TimeKind(tk)
		adj.Status = Status(st)
		out = append(out, &adj)
	}
	return out, rows.Err()
}
