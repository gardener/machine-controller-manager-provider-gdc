// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"testing"
)

func TestMCM(t *testing.T) {
	if cfg.CAFile == "" || cfg.SAFile == "" {
		t.Skip("skipping integration test: -cafile or -service_account not provided")
	}
	if cfg.CommitHash == "" {
		t.Fatal("commit_hash flag is required")
	}
	t.Logf("Starting MCM Presubmit Test [Commit: %s]", cfg.CommitHash)

	// Set a global timeout for the test to prevent hanging CI jobs.
	ctx, cancel := context.WithTimeout(context.Background(), globalTestTimeout)
	defer cancel()

	// Initialize Schemes and Clients
	env, err := bootstrapTestEnv(ctx, t)
	if err != nil {
		t.Fatalf("Failed to bootstrap test environment: %v", err)
	}

	// Install CRDs
	if err := installCRDs(ctx, t, env.VucClient); err != nil {
		t.Fatalf("Failed to install CRDs: %v", err)
	}

	if err := createNamespace(ctx, t, *env); err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}
	t.Logf("Test Namespace Ready: %s", env.Namespace)

	if err = setupRBAC(ctx, t, env); err != nil {
		t.Fatalf("Failed to setup RBAC: %v", err)
	}

	// Create Image Pull Secret
	imagePullSecretName, err := createImagePullSecret(ctx, t, env)
	if err != nil {
		t.Fatalf("Failed to create image pull secret: %v", err)
	}

	// Create Provider Secrets
	if err := createMCMSecret(ctx, t, env); err != nil {
		t.Fatalf("Failed to create credential secret: %v", err)
	}

	err = deployMCM(ctx, t, env, imagePullSecretName)
	if err != nil {
		t.Fatalf("Failed to deploy MCM: %v", err)
	}

	machineDeployment, machineClass, err := triggerProvisioning(ctx, t, env)
	if err != nil {
		t.Fatalf("Failed to trigger provisioning: %v", err)
	}

	// Verify Creation
	verificationCtx, verificationCancel := context.WithTimeout(ctx, verificationTimeout)
	defer verificationCancel()

	if err := verifyProvisioning(verificationCtx, t, env, machineDeployment, machineClass); err != nil {
		t.Fatalf("Verification failed: %v", err)
	}

	t.Log("Test finished successfully")
}
