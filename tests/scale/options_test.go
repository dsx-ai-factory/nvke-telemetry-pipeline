// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package scale

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolveKubeconfig checks single-file selection and override precedence.
func TestResolveKubeconfig(t *testing.T) {
	pathList := "/tmp/first" + string(os.PathListSeparator) + "/tmp/second"
	for _, test := range []struct {
		name      string
		path      string
		args      []string
		want      string
		wantError string
	}{
		{name: "unset has no fallback", wantError: "set KUBECONFIG or pass -kubeconfig"},
		{name: "explicit path", path: "/tmp/custom kubeconfig", want: "/tmp/custom kubeconfig"},
		{name: "relative path", path: "fixtures/kubeconfig", want: "fixtures/kubeconfig"},
		{name: "trailing assignment overrides default", path: "/tmp/default", args: []string{"KUBECONFIG=/tmp/selected kubeconfig"}, want: "/tmp/selected kubeconfig"},
		{name: "multiple files", path: pathList, wantError: "lists multiple files: pass one path"},
		{name: "multiple files in trailing assignment", path: "/tmp/default", args: []string{"KUBECONFIG=" + pathList}, wantError: "lists multiple files: pass one path"},
		{name: "empty list entry", path: "/tmp/first" + string(os.PathListSeparator), wantError: "lists multiple files: pass one path"},
		{name: "single trailing assignment overrides file list", path: pathList, args: []string{"KUBECONFIG=/tmp/selected kubeconfig"}, want: "/tmp/selected kubeconfig"},
		{name: "last trailing assignment wins", args: []string{"KUBECONFIG=" + pathList, "KUBECONFIG=/tmp/selected kubeconfig"}, want: "/tmp/selected kubeconfig"},
		{name: "unknown positional argument", args: []string{"KUBECONFG=/tmp/typo"}, wantError: "unexpected argument"},
		{name: "empty assignment", args: []string{"KUBECONFIG="}, wantError: "unexpected argument"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveKubeconfig(test.path, test.args)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Errorf("resolveKubeconfig() = %q, error = %v, want error containing %q", got, err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveKubeconfig() error = %v", err)
			}
			want, err := filepath.Abs(test.want)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("resolveKubeconfig() = %q, want %q", got, want)
			}
		})
	}
}

// TestRunRequiresPlan checks that invoking the runner without a plan fails early.
func TestRunRequiresPlan(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestRun$", "-test.timeout=5s")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "-plan is required") {
		t.Fatalf("runner without a plan: error = %v, output = %s", err, output)
	}
}

// TestRunRequiresKubeconfig rejects missing credentials before loading a plan.
func TestRunRequiresKubeconfig(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("KUBECONFIG", "")
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestRun$", "-test.timeout=5s", "-plan=missing-plan.yaml")
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "runner without kubeconfig: output = %s", output)
	require.Contains(t, string(output), "set KUBECONFIG or pass -kubeconfig")
}

// TestWithReportFile saves callback output and closes the file on success or failure.
func TestWithReportFile(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "callback failure", err: errors.New("execution failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			const contents = `{"value": 0}`
			var opened *os.File
			calls := 0
			err := withReportFile(path, func(file *os.File) error {
				calls++
				opened = file
				_, err := file.WriteString(contents)
				require.NoError(t, err)
				return test.err
			})
			require.ErrorIs(t, err, test.err)
			require.Equal(t, 1, calls)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, contents, string(data))
			_, err = opened.WriteString("unexpected")
			require.ErrorIs(t, err, os.ErrClosed)
		})
	}
}

// TestWithReportFileDestination rejects unusable destinations before execution.
func TestWithReportFileDestination(t *testing.T) {
	for _, test := range []struct {
		name     string
		path     string
		existing bool
	}{
		{name: "existing file", path: "report.json", existing: true},
		{name: "missing directory", path: "missing/report.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.path)
			const sentinel = "keep existing report"
			if test.existing {
				require.NoError(t, os.WriteFile(path, []byte(sentinel), 0600))
			}
			called := false
			err := withReportFile(path, func(*os.File) error {
				called = true
				return errors.New("unexpected execution")
			})
			require.ErrorContains(t, err, "create report file")
			require.False(t, called, "destination failure must prevent execution")
			if test.existing {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, sentinel, string(data))
			}
		})
	}
}

// TestWithReportFileDisabled still invokes the callback and preserves its error.
func TestWithReportFileDisabled(t *testing.T) {
	want := errors.New("execution failed")
	calls := 0
	err := withReportFile("", func(file *os.File) error {
		calls++
		require.Nil(t, file)
		return want
	})
	require.Equal(t, 1, calls)
	require.ErrorIs(t, err, want)
}
