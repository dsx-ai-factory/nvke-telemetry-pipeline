// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command release-docs resolves images, then refreshes dependency versions, the
// image inventory, and Operator links. Optional -from and -to flags also update
// chart references.
// Run task docs:release from the repository root. It does not bump Chart.yaml,
// stage files, commit, or tag.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tools/internal/release"
)

func main() {
	flag.Usage = func() {
		if _, err := fmt.Fprintln(flag.CommandLine.Output(), "Refresh README dependency versions, the image table, and Operator links. Supply both -from and -to to also update chart-version references."); err != nil {
			log.Fatal(err)
		}
		flag.PrintDefaults()
	}
	root := flag.String("root", "..", "repository root, relative to the working directory")
	from := flag.String("from", "", "old NKTP chart version referenced in documentation")
	to := flag.String("to", "", "new NKTP chart version to reference")
	check := flag.Bool("check", false, "report pending image-table and reference updates without writing")
	printOnly := flag.Bool("print", false, "print only the generated image table without writing")
	flag.Parse()
	log.SetFlags(0)
	if (*from == "") != (*to == "") {
		log.Fatal("supply both -from and -to, or omit both to refresh only the image table and Operator links")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	images, err := release.Inventory(ctx, *root)
	if err != nil {
		log.Fatal(err)
	}
	if *printOnly {
		fmt.Print(images.Table)
		return
	}
	changes, err := release.Plan(*root, *from, *to, images)
	if err == nil {
		err = release.Apply(changes, *check)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Release documentation is current (%d files updated).\n", len(changes))
}
