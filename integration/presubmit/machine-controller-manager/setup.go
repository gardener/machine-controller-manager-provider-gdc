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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gardener/gardener/pkg/component/nodemanagement/machinecontrollermanager"
	machinev1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gardener/machine-controller-manager-provider-gdc/gdc/pkg/auth"
	gdcclient "github.com/gardener/machine-controller-manager-provider-gdc/gdc/pkg/client"
	"github.com/gardener/machine-controller-manager-provider-gdc/integration/pkg/gdc"
	"github.com/gardener/machine-controller-manager-provider-gdc/integration/pkg/gdcloud"
	"github.com/gardener/machine-controller-manager-provider-gdc/integration/pkg/kubernetes"
)

// bootstrapTestEnv initializes clients and configures the GDCloud CLI.
func bootstrapTestEnv(_ context.Context, t *testing.T) (*TestEnv, error) {
	t.Helper()
	// Setup Schemes
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add corev1 to scheme: %v", err)
	}
	if err := extensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add extensionsv1 to scheme: %v", err)
	}
	if err := machinev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add machinev1alpha1 to scheme: %v", err)
	}
	if err := vmv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add vmv1 to scheme: %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add rbacv1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add appsv1 to scheme: %v", err)
	}
	caData, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA file: %v", err)
	}
	saBytes, err := os.ReadFile(cfg.SAFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read SA file: %v", err)
	}
	var sa auth.ServiceAccount
	if err := json.Unmarshal(saBytes, &sa); err != nil {
		return nil, fmt.Errorf("failed to unmarshal SA: %v", err)
	}
	gdcConfig := &gdcclient.OrgClusterConfig{
		OrgClusterURL: fmt.Sprintf("https://management-kube.apiserver.%s.%s.%s", cfg.Org, cfg.Zone, cfg.LabURL),
		CAData:        base64.StdEncoding.EncodeToString(caData),
	}
	// Initialize GDCloud CLI and cluster client
	consoleURL := fmt.Sprintf("https://console.%s.%s.%s", cfg.Org, cfg.Zone, cfg.LabURL)
	gdcClient, err := gdcloud.NewTestingClient(cfg.CAFile, cfg.SAFile, consoleURL)
	if err != nil {
		return nil, fmt.Errorf("gdcloud init failed: %v", err)
	}
	t.Cleanup(func() {
		if err := gdcClient.Cleanup(); err != nil {
			t.Errorf("failed to cleanup gdcloud client: %v", err)
		}
	})
	vucClient, err := gdc.GetUserClusterClient(gdcClient, cfg.Zone, cfg.Project, cfg.VUC, scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to create VUC client: %v", err)
	}
	mgmtClient, err := gdcclient.Get(gdcConfig, &sa, scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to create mgmt client: %v", err)
	}
	namespace := fmt.Sprintf("mcm-e2e-%s", cfg.CommitHash)
	return &TestEnv{
		VucClient:  vucClient,
		MgmtClient: mgmtClient,
		GDCConfig:  gdcConfig,
		Project:    cfg.Project,
		Namespace:  namespace,
		SAName:     "mcm-sa",
	}, nil
}

// installCRDs deploys MCM CRDs using the Gardener Component library.
func installCRDs(ctx context.Context, t *testing.T, vucClient client.WithWatch) error {
	t.Helper()
	t.Log("Installing MCM CRDs...")

	mcmDeployer, err := machinecontrollermanager.NewCRD(vucClient)
	if err != nil {
		return fmt.Errorf("failed to create CRD deployer: %v", err)
	}
	t.Cleanup(func() {
		t.Log("Cleaning up Machine CRDs...")
		// Use Background context to ensure cleanup runs even if test context is cancelled
		// We use a short timeout for cleanup to avoid blocking indefinitely
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := mcmDeployer.Destroy(cleanupCtx); err != nil {
			t.Logf("Failed to destroy CRDs: %v", err)
		}
	})

	if err := mcmDeployer.Deploy(ctx); err != nil {
		return fmt.Errorf("failed to deploy CRDs: %v", err)
	}
	return nil
}

// createNamespace creates the test namespace and registers its cleanup.
func createNamespace(ctx context.Context, t *testing.T, testEnv TestEnv) error {
	t.Helper()

	t.Cleanup(func() {
		t.Logf("Cleaning up Namespace in VUC: %s", testEnv.Namespace)
		// Ensure cleanup uses a fresh context so it runs even if the main test context timed out
		kubernetes.CleanupResources(t, testEnv.VucClient, testEnv.Namespace)
	})

	t.Logf("Creating Namespace in VUC: %s", testEnv.Namespace)
	if err := kubernetes.CreateNamespace(ctx, testEnv.VucClient, testEnv.Namespace); err != nil {
		return err
	}
	return nil
}

func createMCMSecret(ctx context.Context, t *testing.T, testEnv *TestEnv) error {
	t.Helper()

	saBytes, err := os.ReadFile(cfg.SAFile)
	if err != nil {
		return fmt.Errorf("failed to read SA file: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcmSecretName,
			Namespace: testEnv.Namespace,
		},
		// The driver looks for these specific keys
		Data: map[string][]byte{
			"serviceaccount.json": saBytes,
			"userData":            []byte(dummyUserData),
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), secret)
		if err != nil {
			t.Logf("Failed to delete Credential Secret: %v", err)
		}
	})

	t.Logf("Creating Credential Secret: %s", secret.Name)
	if err := testEnv.VucClient.Create(ctx, secret); err != nil {
		return err
	}
	return nil
}

func setupRBAC(ctx context.Context, t *testing.T, testEnv *TestEnv) error {
	// 1. Create Service Account
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEnv.SAName,
			Namespace: testEnv.Namespace,
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), sa)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("Failed to delete ServiceAccount: %v", err)
		}
	})

	if err := testEnv.VucClient.Create(ctx, sa); err != nil {
		return fmt.Errorf("failed to create ServiceAccount:  %w", err)
	}

	// 2. Create Shoot Cluster RBAC (Applied to VUC in this test)
	// In a production environment, these permissions are applied to the Target Cluster (Shoot).
	// They grant MCM access to manage Nodes, handle evictions, and observe workloads during maintenance.
	shootCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("mcm-shoot-role-%s", testEnv.Namespace),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes", "nodes/status", "endpoints", "replicationcontrollers", "pods", "persistentvolumes", "persistentvolumeclaims"},
				Verbs:     []string{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch", "create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods/eviction"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets", "statefulsets", "daemonsets", "deployments"},
				Verbs:     []string{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
			},
			{
				APIGroups: []string{"policy"},
				Resources: []string{"poddisruptionbudgets"},
				Verbs:     []string{"list", "watch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"volumeattachments"},
				Verbs:     []string{"delete", "get", "list", "watch"},
			},
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), shootCR)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("Failed to delete Shoot ClusterRole: %v", err)
		}
	})

	if err := testEnv.VucClient.Create(ctx, shootCR); err != nil {
		return fmt.Errorf("failed to create Shoot ClusterRole: %w", err)
	}

	shootCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("mcm-shoot-binding-%s", testEnv.Namespace),
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: testEnv.SAName, Namespace: testEnv.Namespace},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     shootCR.Name,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), shootCRB)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("Failed to delete Shoot ClusterRoleBinding: %v", err)
		}
	})

	if err := testEnv.VucClient.Create(ctx, shootCRB); err != nil {
		return fmt.Errorf("failed to create Shoot ClusterRoleBinding: %w", err)
	}

	// 3. Create Seed Cluster RBAC (Applied to VUC in this test)
	// In a production environment, these permissions are applied to the Control Plane Cluster (Seed).
	// They grant MCM access to manage Machine resources and handle leader election in its namespace.
	seedRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcm-seed-role",
			Namespace: testEnv.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{machinev1alpha1.GroupName},
				Resources: []string{
					"machineclasses", "machineclasses/status",
					"machinedeployments", "machinedeployments/status",
					"machines", "machines/status",
					"machinesets", "machinesets/status",
				},
				Verbs: []string{"create", "get", "list", "patch", "update", "watch", "delete", "deletecollection"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps", "secrets", "endpoints", "events", "pods"},
				Verbs:     []string{"create", "get", "list", "patch", "update", "watch", "delete", "deletecollection"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups:     []string{"coordination.k8s.io"},
				Resources:     []string{"leases"},
				Verbs:         []string{"get", "watch", "update"},
				ResourceNames: []string{"machine-controller", "machine-controller-manager"},
			},
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), seedRole)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("Failed to delete Seed Role: %v", err)
		}
	})

	if err := testEnv.VucClient.Create(ctx, seedRole); err != nil {
		return fmt.Errorf("failed to create Seed Role: %w", err)
	}

	seedRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcm-seed-binding",
			Namespace: testEnv.Namespace,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: testEnv.SAName, Namespace: testEnv.Namespace},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     seedRole.Name,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), seedRB)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("Failed to delete Seed RoleBinding: %v", err)
		}
	})

	if err := testEnv.VucClient.Create(ctx, seedRB); err != nil {
		return fmt.Errorf("failed to create Seed RoleBinding: %w", err)
	}

	return nil
}

func createImagePullSecret(ctx context.Context, t *testing.T, testEnv *TestEnv) (string, error) {
	t.Helper()

	if cfg.ImagePullCredential == "" {
		return "", fmt.Errorf("image_pull_credential flag is required")
	}

	credBytes, err := os.ReadFile(cfg.ImagePullCredential)
	if err != nil {
		return "", fmt.Errorf("failed to read image pull credential: %w", err)
	}

	secretName := fmt.Sprintf("harbor-registry-%s", cfg.CommitHash)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: testEnv.Namespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: credBytes,
		},
	}

	t.Cleanup(func() {
		err := testEnv.VucClient.Delete(context.Background(), secret)
		if err != nil && !errors.IsNotFound(err) {
			t.Logf("Failed to delete Image Pull Secret: %v", err)
		}
	})

	t.Logf("Creating Image Pull Secret: %s", secretName)
	if err := testEnv.VucClient.Create(ctx, secret); err != nil {
		return "", fmt.Errorf("failed to create image pull secret: %w", err)
	}

	return secretName, nil
}

func deployMCM(ctx context.Context, t *testing.T, testEnv *TestEnv, imagePullSecretName string) error {
	t.Logf("start deploying MCM deployment : %s ", mcmAppName)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcmAppName,
			Namespace: testEnv.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": mcmAppName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": mcmAppName},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: testEnv.SAName,
					ImagePullSecrets: []corev1.LocalObjectReference{
						{Name: imagePullSecretName},
					},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
					},
					Containers: []corev1.Container{
						{
							Name:            "machine-controller-manager",
							Image:           cfg.MCMImageTag,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"/machine-controller-manager"},
							Args: []string{
								fmt.Sprintf("--namespace=%s", testEnv.Namespace),
								"--port=10258",
								"--safety-up=2",
								"--safety-down=1",
								"--leader-elect=false",
								"--v=3",
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/healthz",
										Port:   intstr.FromInt(10258),
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       10,
								FailureThreshold:    3,
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "metrics",
									ContainerPort: 10258,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("32Mi"),
								},
							},
						},
						{
							Name:  "machine-controller-manager-gdch",
							Image: cfg.GdcMCMImageTag,
							Args: []string{
								fmt.Sprintf("--namespace=%s", testEnv.Namespace),
								"--leader-elect=false",
								"--v=3",
								"--machine-creation-timeout=120m",
								"--machine-drain-timeout=5m",
								"--machine-health-timeout=10m",
							},
							ImagePullPolicy: corev1.PullAlways,
						},
					},
				},
			},
		},
	}

	t.Cleanup(func() {
		t.Log("Deleting MCM Deployment...")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := testEnv.VucClient.Delete(ctx, deployment); client.IgnoreNotFound(err) != nil {
			t.Logf("Error deleting MCM Deployment: %v", err)
		}
	})

	if err := testEnv.VucClient.Create(ctx, deployment); err != nil {
		return fmt.Errorf("failed to create Deployment: %w", err)
	}

	// Wait for Readiness
	t.Log("Waiting for MCM Deployment to be Ready...")
	err := kubernetes.WaitForDeploymentReady(ctx, testEnv.VucClient, testEnv.Namespace, mcmAppName, provisioningTimeout)
	if err != nil {
		return fmt.Errorf("failed to wait for MCM Deployment to be Ready: %w", err)
	}
	pods := &corev1.PodList{}
	if err := testEnv.VucClient.List(ctx, pods,
		client.InNamespace(testEnv.Namespace),
		client.MatchingLabels{"app": "machine-controller-manager"}); err != nil {
		return fmt.Errorf("cannot list pods for MCM deployment in namespace %q, %v", testEnv.Namespace, err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("error: no pods found with label app=%s", mcmAppName)
	}

	for _, p := range pods.Items {
		if err := kubernetes.WaitForPodReady(ctx, testEnv.VucClient, testEnv.Namespace, p.Name, time.Minute); err != nil {
			return fmt.Errorf("pod %q in %q namespace is not Ready in one minute, %v", p.Name, testEnv.Namespace, err)
		}
	}

	return nil
}
