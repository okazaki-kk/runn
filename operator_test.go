package runn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/golang-sql/sqlexp/nest"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/k1LoW/httpstub"
	"github.com/k1LoW/runn/testutil"
	"github.com/k1LoW/stopw"
)

func TestExpand(t *testing.T) {
	tests := []struct {
		steps []map[string]interface{}
		vars  map[string]interface{}
		in    interface{}
		want  interface{}
	}{
		{
			[]map[string]interface{}{},
			map[string]interface{}{},
			map[string]string{"key": "val"},
			map[string]interface{}{"key": "val"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"one": "ichi"},
			map[string]string{"key": "{{ vars.one }}"},
			map[string]interface{}{"key": "ichi"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"one": "ichi"},
			map[string]string{"{{ vars.one }}": "val"},
			map[string]interface{}{"ichi": "val"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"one": 1},
			map[string]string{"key": "{{ vars.one }}"},
			map[string]interface{}{"key": uint64(1)},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"one": 1},
			map[string]string{"key": "{{ vars.one + 1 }}"},
			map[string]interface{}{"key": uint64(2)},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"one": 1},
			map[string]string{"key": "{{ string(vars.one) }}"},
			map[string]interface{}{"key": "1"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"one": "01"},
			map[string]string{"path/{{ vars.one }}": "value"},
			map[string]interface{}{"path/01": "value"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"year": 2022},
			map[string]string{"path?year={{ vars.year }}": "value"},
			map[string]interface{}{"path?year=2022": "value"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"boolean": true},
			map[string]string{"boolean": "{{ vars.boolean }}"},
			map[string]interface{}{"boolean": true},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"map": map[string]interface{}{"foo": "test", "bar": 1}},
			map[string]string{"map": "{{ vars.map }}"},
			map[string]interface{}{"map": map[string]interface{}{"foo": "test", "bar": uint64(1)}},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"array": []interface{}{map[string]interface{}{"foo": "test1", "bar": 1}, map[string]interface{}{"foo": "test2", "bar": 2}}},
			map[string]string{"array": "{{ vars.array }}"},
			map[string]interface{}{"array": []interface{}{map[string]interface{}{"foo": "test1", "bar": uint64(1)}, map[string]interface{}{"foo": "test2", "bar": uint64(2)}}},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"float": float64(1)},
			map[string]string{"float": "{{ vars.float }}"},
			map[string]interface{}{"float": uint64(1)},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"float": float64(1.01)},
			map[string]string{"float": "{{ vars.float }}"},
			map[string]interface{}{"float": 1.01},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"float": float64(1.00)},
			map[string]string{"float": "{{ vars.float }}"},
			map[string]interface{}{"float": uint64(1)},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"float": float64(-0.9)},
			map[string]string{"float": "{{ vars.float }}"},
			map[string]interface{}{"float": -0.9},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"escape": "C++"},
			map[string]string{"escape": "{{ urlencode(vars.escape) }}"},
			map[string]interface{}{"escape": "C%2B%2B"},
		},
		{
			[]map[string]interface{}{},
			map[string]interface{}{"uint64": uint64(4600)},
			map[string]string{"uint64": "{{ vars.uint64 }}"},
			map[string]interface{}{"uint64": uint64(4600)},
		},
	}
	for _, tt := range tests {
		o, err := New()
		if err != nil {
			t.Fatal(err)
		}
		o.store.steps = tt.steps
		o.store.vars = tt.vars

		got, err := o.expandBeforeRecord(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(got, tt.want, nil); diff != "" {
			t.Errorf("%s", diff)
		}
	}
}

func TestNewOption(t *testing.T) {
	tests := []struct {
		opts    []Option
		wantErr bool
	}{
		{
			[]Option{Book("testdata/book/book.yml"), Runner("db", "sqlite://path/to/test.db")},
			false,
		},
		{
			[]Option{Runner("db", "sqlite://path/to/test.db"), Book("testdata/book/book.yml")},
			false,
		},
		{
			[]Option{Book("testdata/book/notfound.yml")},
			true,
		},
		{
			[]Option{Runner("db", "unsupported://hostname")},
			true,
		},
		{
			[]Option{Runner("db", "sqlite://path/to/test.db"), HTTPRunner("db", "https://api.github.com", nil)},
			true,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			_, err := New(tt.opts...)
			got := (err != nil)
			if got != tt.wantErr {
				t.Errorf("got %v\nwant %v", got, tt.wantErr)
			}
		})
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/db.yml"},
		{"testdata/book/only_if_included.yml"},
		{"testdata/book/if.yml"},
		{"testdata/book/previous.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.book, func(t *testing.T) {
			db, _ := testutil.SQLite(t)
			o, err := New(Book(tt.book), DBRunner("db", db))
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Run(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestRunAsT(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/db.yml"},
		{"testdata/book/only_if_included.yml"},
		{"testdata/book/if.yml"},
		{"testdata/book/previous.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.book, func(t *testing.T) {
			db, _ := testutil.SQLite(t)
			o, err := New(Book(tt.book), DBRunner("db", db))
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Run(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestRunUsingLoop(t *testing.T) {
	ts := httpstub.NewServer(t)
	counter := 0
	ts.Method(http.MethodGet).Handler(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(fmt.Sprintf("%d", counter))); err != nil {
			t.Fatal(err)
		}
		counter += 1
	})
	t.Cleanup(func() {
		ts.Close()
	})

	tests := []struct {
		book string
	}{
		{"testdata/book/loop.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		o, err := New(T(t), Book(tt.book), Runner("req", ts.Server().URL))
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Run(ctx); err != nil {
			t.Error(err)
		}
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		paths    string
		RUNN_RUN string
		want     int
	}{
		{
			"testdata/book/**/*",
			"",
			func() int {
				e, err := os.ReadDir("testdata/book/")
				if err != nil {
					t.Fatal(err)
				}
				return len(e)
			}(),
		},
		{"testdata/book/**/*", "initdb", 1},
		{"testdata/book/**/*", "nonexistent", 0},
	}
	for _, tt := range tests {
		t.Setenv("RUNN_RUN", tt.RUNN_RUN)
		opts := []Option{
			Runner("req", "https://api.github.com"),
			Runner("db", "sqlite://path/to/test.db"),
			Runner("sc", "sqlite://path/to/test.db"), // dummy
		}
		ops, err := Load(tt.paths, opts...)
		if err != nil {
			t.Fatal(err)
		}
		got := len(ops.ops)
		if got != tt.want {
			t.Errorf("got %v\nwant %v", got, tt.want)
		}
	}
}

func TestRunN(t *testing.T) {
	tests := []struct {
		paths    string
		RUNN_RUN string
		failFast bool
		want     *runNResult
	}{
		{"testdata/book/runn_*", "", false, newRunNResult(t, 4, 2, 1, 1, map[string]string{
			"testdata/book/runn_0_success.yml": resultSuccess,
			"testdata/book/runn_1_fail.yml":    resultFailure,
			"testdata/book/runn_2_success.yml": resultSuccess,
			"testdata/book/runn_3.skip.yml":    resultSuccess,
		})},
		{"testdata/book/runn_*", "", true, newRunNResult(t, 4, 1, 1, 0, map[string]string{
			"testdata/book/runn_0_success.yml": resultSuccess,
			"testdata/book/runn_1_fail.yml":    resultFailure,
		})},
		{"testdata/book/runn_*", "runn_0", false, newRunNResult(t, 1, 1, 0, 0, map[string]string{
			"testdata/book/runn_0_success.yml": resultSuccess,
		})},
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Setenv("RUNN_RUN", tt.RUNN_RUN)
		ops, err := Load(tt.paths, FailFast(tt.failFast))
		if err != nil {
			t.Fatal(err)
		}
		_ = ops.RunN(ctx)
		got := ops.Result().Simplify()
		want := tt.want.Simplify()
		if diff := cmp.Diff(got, want, nil); diff != "" {
			t.Errorf("%s", diff)
		}
	}
}

func TestSkipIncluded(t *testing.T) {
	tests := []struct {
		paths        string
		skipIncluded bool
		want         int
	}{
		{"testdata/book/include_*", false, 3},
		{"testdata/book/include_*", true, 1},
	}
	for _, tt := range tests {
		ops, err := Load(tt.paths, SkipIncluded(tt.skipIncluded), Runner("req", "https://api.github.com"), Runner("db", "sqlite://path/to/test.db"))
		if err != nil {
			t.Fatal(err)
		}
		got := len(ops.ops)
		if got != tt.want {
			t.Errorf("got %v\nwant %v", got, tt.want)
		}
	}
}

func TestSkipTest(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/skip_test.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		o, err := New(Book(tt.book))
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Run(ctx); err != nil {
			t.Error(err)
		}
	}
}

func TestHookFuncTest(t *testing.T) {
	count := 0
	tests := []struct {
		book        string
		beforeFuncs []func(*RunResult) error
		afterFuncs  []func(*RunResult) error
		want        int
	}{
		{"testdata/book/skip_test.yml", nil, nil, 0},
		{
			"testdata/book/skip_test.yml",
			[]func(*RunResult) error{
				func(*RunResult) error {
					count += 3
					return nil
				},
				func(*RunResult) error {
					count = count * 2
					return nil
				},
			},
			[]func(*RunResult) error{
				func(*RunResult) error {
					count += 7
					return nil
				},
			},
			13,
		},
	}
	ctx := context.Background()
	for _, tt := range tests {
		count = 0
		opts := []Option{
			Book(tt.book),
		}
		for _, fn := range tt.beforeFuncs {
			opts = append(opts, BeforeFunc(fn))
		}
		for _, fn := range tt.afterFuncs {
			opts = append(opts, AfterFunc(fn))
		}
		o, err := New(opts...)
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Run(ctx); err != nil {
			t.Error(err)
		}
		if count != tt.want {
			t.Errorf("got %v\nwant %v", count, tt.want)
		}
	}
}

func TestInclude(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/include_main.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		o, err := New(Book(tt.book), Func("upcase", strings.ToUpper))
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Run(ctx); err != nil {
			t.Error(err)
		}
	}
}

func TestDump(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/dump.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		o, err := New(Book(tt.book), Func("upcase", strings.ToUpper))
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Run(ctx); err != nil {
			t.Error(err)
		}
	}
}

func TestShard(t *testing.T) {
	tests := []struct {
		n int
	}{
		{2}, {3}, {4}, {5}, {6}, {7}, {11}, {13}, {17}, {99},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("n=%d", tt.n), func(t *testing.T) {
			got := []*operator{}
			opts := []Option{
				Runner("req", "https://api.github.com"),
				Runner("db", "sqlite://path/to/test.db"),
				Runner("sc", "sqlite://path/to/test.db"), // dummy
			}
			all, err := Load("testdata/book/**/*", opts...)
			if err != nil {
				t.Fatal(err)
			}
			sortOperators(all.ops)
			want := all.ops
			for i := 0; i < tt.n; i++ {
				ops, err := Load("testdata/book/**/*", append(opts, RunShard(tt.n, i))...)
				if err != nil {
					t.Fatal(err)
				}
				selected, err := ops.SelectedOperators()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, selected...)
			}
			if len(got) != len(want) {
				t.Errorf("got %v\nwant %v", len(got), len(want))
			}
			sortOperators(got)
			allow := []interface{}{
				operator{}, httpRunner{}, dbRunner{}, grpcRunner{}, cdpRunner{}, sshRunner{},
			}
			ignore := []interface{}{
				step{}, store{}, sql.DB{}, os.File{}, stopw.Span{}, debugger{}, nest.DB{},
			}
			dopts := []cmp.Option{
				cmp.AllowUnexported(allow...),
				cmpopts.IgnoreUnexported(ignore...),
				cmpopts.IgnoreFields(stopw.Span{}, "ID"),
				cmpopts.IgnoreFields(operator{}, "id"),
				cmpopts.IgnoreFields(cdpRunner{}, "ctx"),
				cmpopts.IgnoreFields(cdpRunner{}, "cancel"),
			}
			if diff := cmp.Diff(got, want, dopts...); diff != "" {
				t.Errorf("%s", diff)
			}
		})
	}
}

func TestVars(t *testing.T) {
	tests := []struct {
		opts    []Option
		wantErr bool
	}{
		{
			[]Option{Book("testdata/book/vars.yml"), Var("token", "world")},
			false,
		},
		{
			[]Option{Book("testdata/book/vars.yml")},
			true,
		},
		{
			[]Option{Book("testdata/book/vars_external.yml"), Var("override", "json://../vars.json")},
			false,
		},
		{
			[]Option{Book("testdata/book/vars_external.yml")},
			true,
		},
	}
	ctx := context.Background()
	for _, tt := range tests {
		o, err := New(tt.opts...)
		if err != nil {
			t.Error(err)
		}
		if err := o.Run(ctx); err != nil {
			if !tt.wantErr {
				t.Errorf("got %v\n", err)
			}
			continue
		}
		if tt.wantErr {
			t.Error("want error")
		}
	}
}

func TestHttp(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/http.yml"},
		{"testdata/book/http_not_follow_redirect.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			ts := testutil.HTTPServer(t)
			t.Setenv("TEST_HTTP_END_POINT", ts.URL)
			o, err := New(Book(tt.book))
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Run(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestGrpc(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/grpc.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			t.Parallel()
			ts := testutil.GRPCServer(t, false)
			o, err := New(Book(tt.book), GrpcRunner("greq", ts.Conn()))
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Run(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestAfterFuncAlwaysCall(t *testing.T) {
	tests := []struct {
		book    string
		wantErr bool
	}{
		{"testdata/book/always_success.yml", false},
		{"testdata/book/always_failure.yml", true},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			var rerr error
			called := false
			o, err := New(Book(tt.book), AfterFunc(func(rr *RunResult) error {
				if rr != nil {
					rerr = rr.Err
				}
				called = true
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			rrerr := o.Run(ctx)
			if (rrerr == nil) != (rerr == nil) {
				t.Errorf("o.Run(ctx) error: %v\nafterFunc got error:%v", rrerr, rerr)
			}
			if !called {
				t.Error("called should be true")
			}
			if rerr != nil && !tt.wantErr {
				t.Errorf("got err: %s", err)
			}
			if rerr == nil && tt.wantErr {
				t.Error("want err")
			}
		})
	}
}

func TestBeforeFuncErr(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/always_success.yml"},
		{"testdata/book/always_failure.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			o, err := New(Book(tt.book), BeforeFunc(func(*RunResult) error {
				return errors.New("before func error")
			}))
			if err != nil {
				t.Fatal(err)
			}
			if got := o.Run(ctx); got != nil {
				if errors.As(got, &BeforeFuncError{}) {
					t.Errorf("got %v\nwant %T", got, &BeforeFuncError{})
				}
				return
			}
			t.Error("want err")
		})
	}
}

func TestAfterFuncErr(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/always_success.yml"},
		{"testdata/book/always_failure.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			o, err := New(Book(tt.book), AfterFunc(func(*RunResult) error {
				return errors.New("after func error")
			}))
			if err != nil {
				t.Fatal(err)
			}
			if got := o.Run(ctx); got != nil {
				if errors.As(got, &AfterFuncError{}) {
					t.Errorf("got %v\nwant %T", got, &AfterFuncError{})
				}
				return
			}
			t.Error("want err")
		})
	}
}

func TestAfterFuncIf(t *testing.T) {
	tests := []struct {
		book    string
		ifCond  string
		wantErr bool
	}{
		{"testdata/book/always_success.yml", "true", true},
		{"testdata/book/always_failure.yml", "true", true},
		{"testdata/book/always_success.yml", "false", false},
		{"testdata/book/always_failure.yml", "false", true},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			o, err := New(Book(tt.book), AfterFuncIf(func(*RunResult) error {
				return errors.New("after func error")
			}, tt.ifCond))
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Run(ctx); err != nil {
				if !tt.wantErr {
					t.Errorf("got %v\nwant nil", err)
				}
				return
			}
			if tt.wantErr {
				t.Error("want err")
			}
		})
	}
}

func TestStoreKeys(t *testing.T) {
	tests := []struct {
		book string
	}{
		{"testdata/book/store_keys.yml"},
	}
	ctx := context.Background()
	for _, tt := range tests {
		tt := tt
		t.Run(tt.book, func(t *testing.T) {
			ts := testutil.HTTPServer(t)
			t.Setenv("TEST_HTTP_END_POINT", ts.URL)
			o, err := New(Book(tt.book))
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Run(ctx); err != nil {
				t.Error(err)
			}
		})
	}
}

func newRunNResult(t *testing.T, total, success, failure, skipped int64, results map[string]string) *runNResult {
	r := &runNResult{}
	r.Total.Add(total)
	r.Success.Add(success)
	r.Failure.Add(failure)
	r.Skipped.Add(skipped)
	for k, v := range results {
		rr := &RunResult{}
		switch v {
		case resultSuccess:
		case resultFailure:
			rr.Err = errors.New("dummy error")
		case resultSkipped:
			rr.Skipped = true
		}
		r.RunResults.Store(k, rr)
	}
	return r
}
