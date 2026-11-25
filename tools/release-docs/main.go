// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command release-docs prints the image inventory and previews documentation
// updates. Pass -write to refresh dependency versions, the image inventory, and
// Operator links. Optional -from and -to flags also update chart references.
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
		if _, err := fmt.Fprintln(flag.CommandLine.Output(), "Print the image table and preview documentation updates. Pass -write to apply them. Supply both -from and -to to also update chart-version references."); err != nil {
			log.Fatal(err)
		}
		flag.PrintDefaults()
	}
	root := flag.String("root", "..", "repository root, relative to the working directory")
	from := flag.String("from", "", "old NKTP chart version referenced in documentation")
	to := flag.String("to", "", "new NKTP chart version to reference")
	write := flag.Bool("write", false, "write the generated documentation updates")
	kubeconfig := flag.String("kubeconfig", "", "validate Running Pod images using this kubeconfig (requires -namespace)")
	namespace := flag.String("namespace", "", "installed release namespace to validate (requires -kubeconfig)")
	flag.Parse()
	log.SetFlags(0)
	if (*from == "") != (*to == "") {
		log.Fatal("supply both -from and -to, or omit both to refresh only the image table and Operator links")
	}
	if (*kubeconfig == "") != (*namespace == "") {
		log.Fatal("supply both -kubeconfig and -namespace, or omit both to skip cluster validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	images, err := release.Inventory(ctx, *root)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(images.Table)
	if *kubeconfig != "" {
		if err := images.ValidateRunningPods(ctx, *kubeconfig, *namespace); err != nil {
			log.Fatal(err)
		}
	}
	changes, err := release.Plan(*root, *from, *to, images)
	if err != nil {
		log.Fatal(err)
	}
	if !*write {
		fmt.Printf("\n%d files would be updated. Use -write to apply changes.\n", len(changes))
		return
	}
	if err := release.Apply(changes); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Release documentation is current (%d files updated).\n", len(changes))
}
