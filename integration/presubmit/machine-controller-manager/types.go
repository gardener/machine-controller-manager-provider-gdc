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
	"flag"
	"time"

	gdcclient "github.com/gardener/machine-controller-manager-provider-gdc/gdc/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Timeouts
	globalTestTimeout   = 15 * time.Minute
	provisioningTimeout = 5 * time.Minute
	cleanupTimeout      = 10 * time.Minute
	verificationTimeout = 10 * time.Minute
	// Resource Names
	mcmSecretName = "mcm-presubmit-secret"
	mcmAppName    = "machine-controller-manager"
	dummyUserData = `
        #cloud-config
        runcmd:
        - echo 'Hello from MCM Presubmit Test'
        - touch /tmp/mcm-test-verification
    `
)

type Config struct {
	CommitHash          string
	Zone                string
	Region              string
	Project             string
	VUC                 string
	Org                 string
	LabURL              string
	CAFile              string
	SAFile              string
	ImagePullCredential string
	GdcMCMImageTag      string
	MCMImageTag         string
	RegistryURL         string
	MachineType         string
	MachineImage        string
}

// GDCProviderSpec defines the structure for the MachineClass ProviderSpec.
type GDCProviderSpec struct {
	Name                 string            `json:"name"`
	Project              string            `json:"project"`
	MachineType          string            `json:"machineType"`
	AccessEnabled        bool              `json:"accessEnabled"`
	CAData               string            `json:"caData"`
	RegistryURL          string            `json:"registryURL"`
	OrgClusterURL        string            `json:"orgClusterURL"`
	SubnetName           string            `json:"subnetName"`
	Disks                []GDCProviderDisk `json:"disks"`
	CredentialsSecretRef map[string]string `json:"credentialsSecretRef"`
	Labels               map[string]string `json:"labels"`
	Annotations          map[string]string `json:"annotations"`
}
type GDCProviderDisk struct {
	Boot       bool              `json:"boot"`
	AutoDelete bool              `json:"autoDelete"`
	SizeGb     int               `json:"sizeGb"`
	Type       string            `json:"type"`
	Image      string            `json:"image"`
	Project    string            `json:"project"`
	Labels     map[string]string `json:"labels"`
}

// TestEnv holds the runtime test environment (clients, config, etc.)
type TestEnv struct {
	VucClient  client.WithWatch
	MgmtClient client.WithWatch
	GDCConfig  *gdcclient.OrgClusterConfig
	Namespace  string
	Project    string
	SAName     string
}

var cfg Config

func init() {
	flag.StringVar(&cfg.CommitHash, "commit_hash", "dummy", "Short commit hash for git repo")
	flag.StringVar(&cfg.Zone, "zone", "", "Zone where VUC is deployed")
	flag.StringVar(&cfg.Region, "region", "", "Region where VUC is deployed")
	flag.StringVar(&cfg.Project, "project", "", "Project where VUC is created")
	flag.StringVar(&cfg.VUC, "vuc", "", "VUC name")
	flag.StringVar(&cfg.Org, "org", "", "Org name for GDC-AG lab")
	flag.StringVar(&cfg.LabURL, "lab_url", "", "Lab URL (e.g., staging.gpcdemolabs.com)")
	flag.StringVar(&cfg.CAFile, "cafile", "", "Path to CA file")
	flag.StringVar(&cfg.SAFile, "service_account", "", "Path to Service Account file")
	flag.StringVar(&cfg.ImagePullCredential, "image_pull_credential", "", "Path to config.json for images")
	flag.StringVar(&cfg.GdcMCMImageTag, "gdc_mcm_image_tag", "", "MCM image tag (image:tag)")
	flag.StringVar(&cfg.MCMImageTag, "mcm_image_tag", "", "MCM image tag (image:tag)")
	flag.StringVar(&cfg.RegistryURL, "registry_url", "", "Harbor Registry URL")
	flag.StringVar(&cfg.MachineImage, "machine_image", "", "Virtual Machine image")
	flag.StringVar(&cfg.MachineType, "machine_type", "", "Virtual Machine type")
}
