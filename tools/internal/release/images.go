// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package release updates release documentation from chart references and renders.
package release

import (
	"bytes"
	"context"
	_ "crypto/sha256" // Register the standard image digest algorithm.
	_ "crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

type imageSet map[string]map[string]bool

// normalized delegates image syntax and Docker Hub shorthand to the registry
// reference parser instead of maintaining a second parser here.
func (images imageSet) normalized() (imageSet, error) {
	result := make(imageSet)
	for image, uses := range images {
		named, err := reference.ParseNormalizedNamed(image)
		if err != nil {
			return nil, fmt.Errorf("invalid image reference %q: %w", image, err)
		}
		for use := range uses {
			result.add(reference.TagNameOnly(named).String(), use)
		}
	}
	return result, nil
}

func (images imageSet) add(image, use string) {
	if images[image] == nil {
		images[image] = make(map[string]bool)
	}
	images[image][use] = true
}

// containers visits standard PodSpec container lists at any nesting depth.
// Native Collector configuration is deliberately opaque to image discovery.
func containers(spec map[string]any) []map[string]any {
	var result []map[string]any
	for key, value := range spec {
		if key == "config" {
			continue
		}
		if child, ok := value.(map[string]any); ok {
			result = append(result, containers(child)...)
		}
		if !slices.Contains([]string{"containers", "initContainers", "ephemeralContainers", "additionalContainers"}, key) {
			continue
		}
		items, _ := value.([]any)
		for _, item := range items {
			result = append(result, object(item))
		}
	}
	return result
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func at(value map[string]any, path ...string) any {
	var result any = value
	for _, key := range path {
		result = object(result)[key]
	}
	return result
}

func text(value any) string {
	result, _ := value.(string)
	return result
}

// discover inventories effective images, including Operator defaults only when a
// Collector actually relies on them. Collector configuration is never traversed.
func discover(data []byte) (imageSet, error) {
	images := make(imageSet)
	defaults := make(map[string]string)
	var collectors []map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var resource map[string]any
		if err := decoder.Decode(&resource); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode rendered manifest: %w", err)
		}
		if resource == nil {
			continue
		}
		kind := text(resource["kind"])
		use := "Default workload"
		hooks := text(at(resource, "metadata", "annotations", "helm.sh/hook"))
		if hooks != "" {
			runsWithRelease := false
			for _, hook := range strings.Split(hooks, ",") {
				switch strings.TrimSpace(hook) {
				case "pre-install", "post-install", "pre-upgrade", "post-upgrade", "pre-delete", "post-delete", "pre-rollback", "post-rollback":
					runsWithRelease = true
				}
			}
			if !runsWithRelease {
				continue
			}
			use = "Helm hook: " + hooks
			if text(at(resource, "metadata", "labels", "app.kubernetes.io/name")) == "nktp-namespace-labeling" {
				use = "Runs before install or upgrade when `namespace.labeling.enabled=true`"
			}
		} else {
			component := text(at(resource, "metadata", "labels", "app.kubernetes.io/name"))
			if text(at(resource, "metadata", "labels", "app.kubernetes.io/component")) == "kube-state-metrics" {
				component = "kube-state-metrics"
			}
			if text(at(resource, "spec", "template", "metadata", "labels", "app")) == "lgtm" {
				component = "otel-lgtm"
			}
			switch component {
			case "node-exporter", "metrics-server", "blackbox-exporter", "kube-state-metrics", "otel-lgtm":
				use = "Runs only when `" + component + ".enabled=true`"
			}
		}
		if kind == "OpenTelemetryCollector" {
			collectors = append(collectors, resource)
			use = "Collector sidecar"
		}
		isOperator := kind == "Deployment" && text(at(resource, "metadata", "labels", "app.kubernetes.io/name")) == "opentelemetry-operator"
		if isOperator {
			use = "Operator"
		}
		for _, container := range containers(object(resource["spec"])) {
			if image := text(container["image"]); image != "" {
				images.add(image, use)
			}
			if isOperator {
				args, _ := container["args"].([]any)
				for index, arg := range args {
					flag, image, joined := strings.Cut(text(arg), "=")
					if flag != "--collector-image" && flag != "--target-allocator-image" {
						continue
					}
					if !joined && index+1 < len(args) {
						image = text(args[index+1])
					}
					if previous := defaults[flag]; previous != "" && previous != image {
						return nil, fmt.Errorf("conflicting Operator defaults for %s", flag)
					}
					defaults[flag] = image
				}
			}
		}
		if kind == "OpAMPBridge" || kind == "TargetAllocator" {
			if image := text(at(resource, "spec", "image")); image != "" {
				images.add(image, kind)
			}
		}
	}
	for _, collector := range collectors {
		name := text(at(collector, "metadata", "name"))
		image := text(at(collector, "spec", "image"))
		if image == "" {
			image = defaults["--collector-image"]
		}
		if image == "" {
			return nil, fmt.Errorf("cannot resolve Collector image for %s; configure an explicit image when using an external Operator", name)
		}
		images.add(image, "Collector")
		if enabled, _ := at(collector, "spec", "targetAllocator", "enabled").(bool); enabled {
			image = text(at(collector, "spec", "targetAllocator", "image"))
			if image == "" {
				image = defaults["--target-allocator-image"]
			}
			if image == "" {
				return nil, fmt.Errorf("cannot resolve Target Allocator image for %s; configure an explicit image when using an external Operator", name)
			}
			images.add(image, "Target Allocator")
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("rendered chart contains no images")
	}
	return images.normalized()
}

func render(ctx context.Context, root string) (imageSet, error) {
	args := []string{"template", "nktp", filepath.Join(root, "nktp"), "--set", "metrics-server.enabled=true", "--set", "blackbox-exporter.enabled=true", "--set", "namespace.labeling.enabled=true"}
	command := exec.CommandContext(ctx, "helm", args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("helm template: %w\n%s", err, stderr.String())
	}
	return discover(data)
}

// ResolvedImages holds the rendered inventory and Operator images used to
// resolve documentation references during release preparation.
type ResolvedImages struct {
	Table          string
	operatorImages []string
	images         imageSet
}

// ValidateRunningPods reads the installed namespace without changing the cluster
// and rejects Pod image declarations absent from the rendered inventory.
func (images ResolvedImages) ValidateRunningPods(ctx context.Context, kubeconfig, namespace string) error {
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig,
		"--namespace", namespace, "get", "pods", "--field-selector=status.phase=Running",
		"--request-timeout=30s", "-o", "json")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("read Running Pods in namespace %q: %w\n%s", namespace, err, stderr.String())
	}
	return images.validateRunningPods(data, namespace)
}

func (images ResolvedImages) validateRunningPods(data []byte, namespace string) error {
	var pods struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     map[string]any
			Status   struct{ Phase string }
		}
	}
	if err := json.Unmarshal(data, &pods); err != nil {
		return fmt.Errorf("decode Pods in namespace %q: %w", namespace, err)
	}
	observed := make(imageSet)
	running := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase != "Running" {
			continue
		}
		running++
		for _, container := range containers(pod.Spec) {
			observed.add(text(container["image"]), fmt.Sprintf("Pod %s/%s container %s",
				namespace, pod.Metadata.Name, text(container["name"])))
		}
	}
	if running == 0 {
		return fmt.Errorf("no Running Pods found in namespace %q; install the chart before validating", namespace)
	}
	observed, err := observed.normalized()
	if err != nil {
		return fmt.Errorf("pod inventory: %w", err)
	}
	var unexpected []string
	for _, image := range slices.Sorted(maps.Keys(observed)) {
		if _, found := images.images[image]; !found {
			unexpected = append(unexpected, fmt.Sprintf("%s\n    Found in these Pods' containers:\n      %s", image,
				strings.Join(slices.Sorted(maps.Keys(observed[image])), "\n      ")))
		}
	}
	if len(unexpected) > 0 {
		return fmt.Errorf("running Pod images in namespace %q absent from rendered inventory:\n  %s",
			namespace, strings.Join(unexpected, "\n  "))
	}
	return nil
}

func (images ResolvedImages) operatorVersion() (string, error) {
	version := ""
	for _, image := range images.operatorImages {
		named, err := reference.ParseNormalizedNamed(image)
		tagged, ok := named.(reference.Tagged)
		if err != nil || !ok {
			return "", fmt.Errorf("cannot resolve Operator documentation version from image %q: a semantic version tag is required", image)
		}
		tag := strings.TrimPrefix(tagged.Tag(), "v")
		if !versionPattern.MatchString(tag) {
			return "", fmt.Errorf("operator image %q has no semantic version tag", image)
		}
		if version != "" && version != tag {
			return "", fmt.Errorf("conflicting Operator image versions: %s and %s", version, tag)
		}
		version = tag
	}
	if version == "" {
		return "", fmt.Errorf("cannot resolve Operator documentation version: no Operator image found")
	}
	return version, nil
}

// Inventory resolves chart defaults and optional NKTP components before any
// documentation is updated. Its inputs are rendered manifests, not pinning helpers.
func Inventory(ctx context.Context, root string) (ResolvedImages, error) {
	images, err := render(ctx, root)
	if err != nil {
		return ResolvedImages{}, err
	}
	resolved := ResolvedImages{Table: markdown(images), images: images}
	for image, uses := range images {
		if uses["Operator"] {
			resolved.operatorImages = append(resolved.operatorImages, image)
		}
	}
	return resolved, nil
}

func markdown(images imageSet) string {
	var output strings.Builder
	output.WriteString("| Full image reference | When it runs |\n| --- | --- |\n")
	for _, image := range slices.Sorted(maps.Keys(images)) {
		var uses []string
		for use := range images[image] {
			switch use {
			case "Default workload":
				use = "Runs with the workload that declares it"
			case "Collector":
				use = "Runs in Collector workloads"
				if images[image]["Collector sidecar"] {
					use = "Runs in Collector workloads and their sidecars"
				}
			case "Collector sidecar":
				if images[image]["Collector"] {
					continue
				}
				use = "Runs in Collector sidecars"
			case "Operator":
				use = "Runs only when `opentelemetry-kube-stack.opentelemetry-operator.enabled=true`"
			case "Target Allocator":
				use = "Runs by default for node, large-target, nodeless, and watchdog Collectors"
			case "Helm hook: pre-delete":
				use = "Runs before uninstall"
			default:
				if hooks, ok := strings.CutPrefix(use, "Helm hook: "); ok {
					use = "Runs during Helm hooks: " + hooks
				}
			}
			uses = append(uses, use)
		}
		slices.Sort(uses)
		fmt.Fprintf(&output, "| `%s` | %s |\n", image, strings.Join(uses, "; "))
	}
	return output.String()
}
