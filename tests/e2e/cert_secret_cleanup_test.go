// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestCertificateSecretCleanup(t *testing.T) {
	var certificateSecrets []string

	f := features.NewWithDescription("CertificateSecretCleanup", "Uninstalling the release deletes its Certificate Secrets").
		WithSetup("RecordingCertificateSecrets", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			r := c.Client().Resources(c.Namespace())

			certificates := &unstructured.UnstructuredList{}
			certificates.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "CertificateList"})
			require.NoError(t, r.List(setupCtx, certificates), "listing Certificates")
			for _, certificate := range certificates.Items {
				if len(certificate.GetOwnerReferences()) > 0 {
					continue
				}
				secretName, found, err := unstructured.NestedString(certificate.Object, "spec", "secretName")
				require.NoError(t, err)
				require.True(t, found, "Certificate %s should name its Secret", certificate.GetName())
				certificateSecrets = append(certificateSecrets, secretName)
			}
			require.NotEmpty(t, certificateSecrets, "the release should create Certificates")

			for _, name := range certificateSecrets {
				var secret corev1.Secret
				require.NoError(t, r.Get(setupCtx, name, c.Namespace(), &secret),
					"Secret %s should exist before the release is uninstalled", name)
			}
			return ctx
		}).
		AssessWithDescription("DeletingCertificateSecrets", "Uninstall removes every Certificate Secret",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				uninstallCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				require.NoError(t, testenv.UninstallRelease(uninstallCtx, c), "uninstalling the release")

				r := c.Client().Resources(c.Namespace())
				for _, name := range certificateSecrets {
					var secret corev1.Secret
					err := r.Get(uninstallCtx, name, c.Namespace(), &secret)
					assert.True(t, apierrors.IsNotFound(err), "Secret %s should be deleted by uninstall, got: %v", name, err)
				}

				var job batchv1.Job
				err := r.Get(uninstallCtx, testenv.ReleaseName+"-cert-cleanup", c.Namespace(), &job)
				assert.True(t, apierrors.IsNotFound(err), "the successful cleanup Job should be removed, got: %v", err)
				return ctx
			}).
		WithTeardown("RestoringRelease", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
			defer cancel()
			require.NoError(t, testenv.InstallRelease(restoreCtx), "reinstalling the release")
			_, err := waitForCollectors(restoreCtx, c)
			require.NoError(t, err, "waiting for Collector workloads after reinstall")
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
