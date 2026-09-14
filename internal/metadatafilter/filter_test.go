package metadatafilter

import (
	"encoding/json"
	"testing"
)

func TestTypesNullAndGroups(t *testing.T) {
	f := Filter{All: []Condition{{"confirmation", "eq", "confirmed"}, {"valid_from_order", "lte", 2}}, Any: [][]Condition{{{"valid_to_order", "is_null", nil}, {"valid_to_order", "gt", 2}}}}
	if e := f.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"confirmation":"confirmed","valid_from_order":2}`, true},
		{`{"confirmation":"confirmed","valid_from_order":2,"valid_to_order":null}`, true},
		{`{"confirmation":"confirmed","valid_from_order":2,"valid_to_order":3}`, true},
		{`{"confirmation":"confirmed","valid_from_order":3}`, false},
		{`{"confirmation":"confirmed","valid_from_order":2,"valid_to_order":2}`, false},
		{`{"confirmation":"confirmed","valid_from_order":"2"}`, false},
		{`{"confirmation":"planned","valid_from_order":0}`, false},
		{`{}`, false},
	} {
		if got := f.Matches(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("%s: %v", tc.raw, got)
		}
	}
	for _, c := range []Condition{{"sql", "eq", "select"}, {"novel_id", "like", "x"}, {"valid_from_order", "lte", "2"}, {"hard_constraint", "eq", 1}, {"recorded_at", "lt", "yesterday"}, {"novel_id", "in", []any{}}, {"valid_to_order", "is_null", 1}} {
		if (Filter{All: []Condition{c}}).Validate() == nil {
			t.Errorf("应拒绝 %#v", c)
		}
	}
}
