package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateSQL(t *testing.T) {
	ok := []string{
		"SELECT count(*) FROM documents",
		"select title from documents limit 5;",
		"WITH x AS (SELECT id FROM documents) SELECT count(*) FROM x",
		"  -- 注释\n SELECT 1",
		"/* 块注释 */ SELECT 1",
	}
	for _, sqlText := range ok {
		if err := validateSQL(sqlText); err != nil {
			t.Errorf("validateSQL(%q) = %v, want nil", sqlText, err)
		}
	}

	bad := []string{
		"",
		"   ",
		"DELETE FROM documents",
		"UPDATE documents SET title = 'x'",
		"SELECT 1; SELECT 2",
		"SELECT set_config('app.tenant_id','2',true)",
	}
	for _, sqlText := range bad {
		if err := validateSQL(sqlText); err == nil {
			t.Errorf("validateSQL(%q) = nil, want error", sqlText)
		}
	}
}

func TestFirstKeyword(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":            "SELECT",
		"-- c\nselect 1":      "SELECT",
		"/* c */ With x AS (": "WITH",
	}
	for in, want := range cases {
		got, ok := firstKeyword(in)
		if !ok || got != want {
			t.Errorf("firstKeyword(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

func TestFormatSQLCell(t *testing.T) {
	if got := formatSQLCell(nil); got != "NULL" {
		t.Errorf("nil = %q", got)
	}
	if got := formatSQLCell([]byte("abc")); got != "abc" {
		t.Errorf("bytes = %q", got)
	}
	if got := formatSQLCell(12); got != "12" {
		t.Errorf("int = %q", got)
	}
	long := strings.Repeat("字", sqlCellRunes+10)
	if got := formatSQLCell(long); len([]rune(got)) != sqlCellRunes+1 {
		t.Errorf("truncated length = %d", len([]rune(got)))
	}
}

func TestToolBudgetIsShared(t *testing.T) {
	b := newToolBudget(1)
	if err := b.take(); err != nil {
		t.Fatalf("first take: %v", err)
	}
	if err := b.take(); !errors.Is(err, ErrToolCallLimit) {
		t.Fatalf("second take = %v, want ErrToolCallLimit", err)
	}
}

type fakeSQLRunner struct {
	result    SQLResult
	err       error
	gotTenant int64
	gotQuery  string
	calls     int
}

func (f *fakeSQLRunner) Run(_ context.Context, tenantID int64, query string) (SQLResult, error) {
	f.calls++
	f.gotTenant = tenantID
	f.gotQuery = query
	return f.result, f.err
}

func TestSQLToolRunsWithTenantAndAudits(t *testing.T) {
	runner := &fakeSQLRunner{result: SQLResult{
		Columns: []string{"count"},
		Rows:    [][]string{{"12"}},
	}}
	audits := &auditLog{}
	tool, err := newSQLTool(runner, 7, newToolBudget(3), audits)
	if err != nil {
		t.Fatalf("newSQLTool: %v", err)
	}

	out, err := tool.InvokableRun(context.Background(), `{"sql":"SELECT count(*) FROM documents"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if runner.gotTenant != 7 {
		t.Errorf("tenant = %d, want 7", runner.gotTenant)
	}
	if runner.gotQuery != "SELECT count(*) FROM documents" {
		t.Errorf("query = %q", runner.gotQuery)
	}

	var parsed sqlOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not json: %v", err)
	}
	if parsed.RowCount != 1 || parsed.Rows[0][0] != "12" {
		t.Errorf("output = %+v", parsed)
	}
	items := audits.all()
	if len(items) != 1 || items[0].Kind != "sql" || items[0].RowCount != 1 {
		t.Fatalf("audits = %+v", items)
	}
	if len(items[0].Digest) != 16 {
		t.Errorf("digest = %q, want 16 hex chars", items[0].Digest)
	}
}

func TestSQLToolRecordsFailure(t *testing.T) {
	runner := &fakeSQLRunner{err: ErrSQLDenied}
	audits := &auditLog{}
	tool, err := newSQLTool(runner, 7, newToolBudget(3), audits)
	if err != nil {
		t.Fatalf("newSQLTool: %v", err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"sql":"SELECT * FROM users"}`); err == nil {
		t.Fatal("want error")
	}
	items := audits.all()
	if len(items) != 1 || items[0].Error == "" {
		t.Fatalf("audits = %+v", items)
	}
}
