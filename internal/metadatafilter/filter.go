// Package metadatafilter 定义 metadata_filter_v1 的有界、声明类型的过滤协议。
package metadatafilter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

const Capability = "metadata_filter_v1"
const MaxConditions = 32

type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

// 每个 Any 分组至少匹配一项；所有分组与 All 之间为 AND。禁止递归表达式。
type Filter struct {
	All []Condition   `json:"all,omitempty"`
	Any [][]Condition `json:"any,omitempty"`
}

var fields = map[string]string{
	"novel_id": "string", "branch_id": "string", "confirmation": "string",
	"source_chapter_id": "string", "source_version_id": "string", "source_asset_id": "string", "source_asset_version_id": "string",
	"valid_from_order": "integer", "valid_to_order": "integer", "order_revision": "integer",
	"hard_constraint": "boolean", "current_state": "boolean", "recorded_at": "timestamp",
}

func (f Filter) Empty() bool { return len(f.All) == 0 && len(f.Any) == 0 }

func (f Filter) Validate() error {
	count := len(f.All)
	groups := append([][]Condition{f.All}, f.Any...)
	for _, group := range f.Any {
		if len(group) == 0 {
			return fmt.Errorf("any 分组不能为空")
		}
		count += len(group)
	}
	if count > MaxConditions {
		return fmt.Errorf("过滤条件最多 %d 项", MaxConditions)
	}
	for _, group := range groups {
		for _, c := range group {
			kind, ok := fields[c.Field]
			if !ok {
				return fmt.Errorf("未知过滤字段 %q", c.Field)
			}
			if c.Op == "is_null" || c.Op == "not_null" {
				if c.Value != nil {
					return fmt.Errorf("空值操作不接受 value")
				}
				continue
			}
			if !slices.Contains([]string{"eq", "ne", "in", "lt", "lte", "gt", "gte"}, c.Op) {
				return fmt.Errorf("未知过滤操作符 %q", c.Op)
			}
			if slices.Contains([]string{"lt", "lte", "gt", "gte"}, c.Op) && kind != "integer" && kind != "timestamp" {
				return fmt.Errorf("字段 %s 不支持区间比较", c.Field)
			}
			values := []any{c.Value}
			if c.Op == "in" {
				var ok bool
				values, ok = c.Value.([]any)
				if !ok || len(values) < 1 || len(values) > 32 {
					return fmt.Errorf("in 需要 1 至 32 个同类型值")
				}
			}
			for _, value := range values {
				if !typed(kind, value) {
					return fmt.Errorf("字段 %s 需要 %s 类型", c.Field, kind)
				}
			}
		}
	}
	return nil
}

func number(value any) (float64, bool) {
	var v float64
	switch n := value.(type) {
	case json.Number:
		var e error
		v, e = n.Float64()
		if e != nil {
			return 0, false
		}
	case float64:
		v = n
	case int:
		v = float64(n)
	case int64:
		v = float64(n)
	default:
		return 0, false
	}
	return v, !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 9007199254740991 && math.Trunc(v) == v
}

func typed(kind string, value any) bool {
	switch kind {
	case "integer":
		_, ok := number(value)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		v, ok := value.(string)
		return ok && len(v) > 0 && len(v) <= 512
	case "timestamp":
		v, ok := value.(string)
		if !ok {
			return false
		}
		t, e := time.Parse("2006-01-02T15:04:05Z", v)
		return e == nil && t.Format("2006-01-02T15:04:05Z") == v
	}
	return false
}

func (f Filter) Matches(raw json.RawMessage) bool {
	if f.Empty() {
		return true
	}
	var props map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&props) != nil || props == nil {
		return false
	}
	for _, c := range f.All {
		if !match(c, props[c.Field]) {
			return false
		}
	}
	for _, group := range f.Any {
		ok := false
		for _, c := range group {
			if match(c, props[c.Field]) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func match(c Condition, value any) bool {
	if c.Op == "is_null" {
		return value == nil
	}
	if c.Op == "not_null" {
		return value != nil
	}
	kind := fields[c.Field]
	if !typed(kind, value) {
		return false
	}
	compare := func(want any) int {
		if kind == "integer" {
			a, _ := number(value)
			b, _ := number(want)
			if a < b {
				return -1
			}
			if a > b {
				return 1
			}
			return 0
		}
		if kind == "boolean" {
			if value == want {
				return 0
			}
			return 1
		}
		return strings.Compare(value.(string), want.(string))
	}
	if c.Op == "in" {
		for _, want := range c.Value.([]any) {
			if compare(want) == 0 {
				return true
			}
		}
		return false
	}
	x := compare(c.Value)
	switch c.Op {
	case "eq":
		return x == 0
	case "ne":
		return x != 0
	case "lt":
		return x < 0
	case "lte":
		return x <= 0
	case "gt":
		return x > 0
	case "gte":
		return x >= 0
	}
	return false
}

// SQL 仅接受内部固定列名，字段来自白名单，值全部绑定参数；在候选 LIMIT 前应用。
func (f Filter) SQL(column string) (string, []any, error) {
	if column != "node.properties_json" {
		return "", nil, fmt.Errorf("过滤列无效")
	}
	if e := f.Validate(); e != nil {
		return "", nil, e
	}
	args := []any{}
	condition := func(c Condition) string {
		path := "$." + c.Field
		t := "json_type(" + column + ", '" + path + "')"
		v := "json_extract(" + column + ", '" + path + "')"
		if c.Op == "is_null" {
			return "(" + t + " IS NULL OR " + t + "='null')"
		}
		if c.Op == "not_null" {
			return "(" + t + " IS NOT NULL AND " + t + "<>'null')"
		}
		check := ""
		switch fields[c.Field] {
		case "integer":
			check = t + " IN ('integer','real') AND abs(" + v + ")<=9007199254740991 AND " + v + "=CAST(" + v + " AS INTEGER)"
		case "boolean":
			check = t + " IN ('true','false')"
		case "string":
			check = t + "='text' AND length(CAST(" + v + " AS BLOB)) BETWEEN 1 AND 512"
		case "timestamp":
			check = t + "='text' AND length(" + v + ")=20 AND strftime('%Y-%m-%dT%H:%M:%SZ'," + v + ")=" + v
		}
		values := []any{c.Value}
		op := map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[c.Op]
		if c.Op == "in" {
			values = c.Value.([]any)
			op = "IN"
		}
		marks := []string{}
		for _, value := range values {
			if fields[c.Field] == "integer" {
				value, _ = number(value)
			}
			args = append(args, value)
			marks = append(marks, "?")
		}
		rhs := strings.Join(marks, ",")
		if c.Op == "in" {
			rhs = "(" + rhs + ")"
		}
		return "(" + check + " AND " + v + " " + op + " " + rhs + ")"
	}
	clauses := []string{}
	for _, c := range f.All {
		clauses = append(clauses, condition(c))
	}
	for _, group := range f.Any {
		or := []string{}
		for _, c := range group {
			or = append(or, condition(c))
		}
		clauses = append(clauses, "("+strings.Join(or, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " AND " + strings.Join(clauses, " AND "), args, nil
}
