// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command compare-outage prints a Markdown comparison of two outage reports.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/backend-outage/pkg/outagereport"
)

func main() {
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "Usage: compare-outage REPORT_A.json REPORT_B.json") }
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}
	paths := [2]string{flag.Arg(0), flag.Arg(1)}
	var reports [2]outagereport.Report
	for i, path := range paths {
		r, err := outagereport.Load(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		reports[i] = r
	}
	if _, err := fmt.Print(outagereport.Compare(paths, reports)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
