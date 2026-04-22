package main

import (
	"context"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestParseDescriberCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{in: "./run.sh", want: []string{"./run.sh"}},
		{in: "python3 -u run.py", want: []string{"python3", "-u", "run.py"}},
		{in: `bash -c "echo hi"`, want: []string{"bash", "-c", "echo hi"}},
		{in: `echo 'hello world'`, want: []string{"echo", "hello world"}},
		{in: `echo foo\ bar`, want: []string{"echo", "foo bar"}},
		{in: "", err: true},
		{in: `echo "unterminated`, err: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseDescriberCommand(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDescriberCommand: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestSubprocessDescriberRoundTrip runs a tiny shell command that echoes
// its prompt and image-length back via stdout, confirming env-var plumbing
// and stdin delivery.
func TestSubprocessDescriberRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	d := &subprocessDescriber{
		command: []string{"sh", "-c", `printf 'prompt=%s mime=%s bytes=%s' "$GO_MARKITDOWN_PROMPT" "$GO_MARKITDOWN_MIME" "$(wc -c)"`},
		timeout: 10 * time.Second,
	}
	got, err := d.DescribeImage(context.Background(), []byte("hello"), "image/png", "hi")
	if err != nil {
		t.Fatalf("DescribeImage: %v", err)
	}
	wantPrefix := "prompt=hi mime=image/png bytes="
	if len(got) <= len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestSubprocessDescriberTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	d := &subprocessDescriber{
		command: []string{"sh", "-c", "sleep 5"},
		timeout: 100 * time.Millisecond,
	}
	_, err := d.DescribeImage(context.Background(), []byte("x"), "image/png", "prompt")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
