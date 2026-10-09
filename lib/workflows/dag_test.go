package workflows

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// helmNode builds a minimal valid helm component with the given id, so a test
// can focus on what it's checking rather than config. A helm component carries
// a target cluster + namespace (both required for the type).
func helmNode(id uuid.UUID) ComponentInput {
	target := uuid.New()
	return ComponentInput{
		ID:              id,
		Name:            "n-" + id.String()[:8],
		Type:            TypeHelm,
		TargetClusterID: &target,
		TargetNamespace: "ns",
		Config: map[string]string{
			helmConfigChartSource: helm.SourceOCI,
			helm.ConfigRepoURL:    "oci://example.com/charts/app",
		},
	}
}

// stagesOf builds a workflow with one stage per argument, in order, each
// holding the given components.
func stagesOf(stages ...[]ComponentInput) []StageInput {
	out := make([]StageInput, len(stages))
	for i, comps := range stages {
		out[i] = StageInput{ID: uuid.New(), Name: "stage", Components: comps}
	}
	return out
}

// validateOneStage validates a workflow of a single stage holding nodes — the
// shape every per-component check needs.
func validateOneStage(nodes []ComponentInput) error {
	return validateWorkflow(stagesOf(nodes))
}

func TestValidateWorkflow_RejectsNonSlugName(t *testing.T) {
	// A component name must be a slug — it is referenced by name in output
	// expressions and seeds the default release name.
	for _, name := range []string{"My App", "web_1", "-web", ""} {
		n := helmNode(uuid.New())
		n.Name = name
		if err := validateOneStage([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("name %q: expected ErrInvalidConfig, got %v", name, err)
		}
	}
	// A slug passes.
	n := helmNode(uuid.New())
	n.Name = "web-1"
	if err := validateOneStage([]ComponentInput{n}); err != nil {
		t.Errorf("slug name: expected pass, got %v", err)
	}
}

func TestValidateWorkflow_Empty(t *testing.T) {
	if err := validateOneStage(nil); err != nil {
		t.Fatalf("expected empty workflow to pass, got %v", err)
	}
}

func TestValidateWorkflow_MissingID(t *testing.T) {
	nodes := []ComponentInput{helmNode(uuid.Nil)}
	if err := validateOneStage(nodes); !errors.Is(err, ErrMissingID) {
		t.Fatalf("expected ErrMissingID, got %v", err)
	}
}

func TestValidateWorkflow_DuplicateID(t *testing.T) {
	a := uuid.New()
	nodes := []ComponentInput{helmNode(a), helmNode(a)}
	if err := validateOneStage(nodes); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("expected ErrDuplicateID, got %v", err)
	}
}

func TestValidateConfig_HelmSources(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]string
		wantErr bool
	}{
		{
			name: "http_repo ok",
			config: map[string]string{
				helmConfigChartSource: helm.SourceHTTPRepo,
				helm.ConfigRepoURL:    "https://charts.example.com",
				helm.ConfigChart:      "app",
			},
		},
		{
			name: "http_repo missing chart",
			config: map[string]string{
				helmConfigChartSource: helm.SourceHTTPRepo,
				helm.ConfigRepoURL:    "https://charts.example.com",
			},
			wantErr: true,
		},
		{
			name: "oci ok",
			config: map[string]string{
				helmConfigChartSource: helm.SourceOCI,
				helm.ConfigRepoURL:    "oci://example.com/charts/app",
			},
		},
		{
			name: "git ok",
			config: map[string]string{
				helmConfigChartSource: helm.SourceGit,
				helm.ConfigRepoURL:    "https://github.com/acme/charts",
			},
		},
		{
			name: "git missing repo_url",
			config: map[string]string{
				helmConfigChartSource: helm.SourceGit,
			},
			wantErr: true,
		},
		{
			name:    "missing chart_source",
			config:  map[string]string{helm.ConfigRepoURL: "https://charts.example.com"},
			wantErr: true,
		},
		{
			name: "unknown chart_source",
			config: map[string]string{
				helmConfigChartSource: "ftp",
				helm.ConfigRepoURL:    "ftp://example.com",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := uuid.New()
			n := ComponentInput{ID: uuid.New(), Name: "x", Type: TypeHelm, TargetClusterID: &target, TargetNamespace: "ns", Config: tt.config}
			err := validateOneStage([]ComponentInput{n})
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("expected ErrInvalidConfig, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected pass, got %v", err)
			}
		})
	}
}

func TestValidateConfig_HelmInterpolation(t *testing.T) {
	// A base git-source node with an explicit git_ref; each case mutates it.
	base := func() ComponentInput {
		target := uuid.New()
		return ComponentInput{
			ID: uuid.New(), Name: "web", Type: TypeHelm,
			TargetClusterID: &target, TargetNamespace: "ns",
			Config: map[string]string{
				helmConfigChartSource: helm.SourceGit,
				helm.ConfigRepoURL:    "https://github.com/org/charts.git",
				helm.ConfigGitRef:     "main",
			},
		}
	}
	tests := []struct {
		name    string
		mutate  func(*ComponentInput)
		wantErr string // substring of the error; empty = the node must validate
	}{
		{
			name: "vars in values, namespace, and release name",
			mutate: func(n *ComponentInput) {
				n.Config[helmConfigValues] = "host: ${{ vars.CUSTOMER_ID }}.example.com"
				n.Config[helmConfigReleaseName] = "web-${{ vars.CUSTOMER_ID }}"
				n.TargetNamespace = "customer-${{ vars.CUSTOMER_ID }}"
			},
		},
		{
			name:   "run.git_sha_short in values on a git source",
			mutate: func(n *ComponentInput) { n.Config[helmConfigValues] = "tag: ${{ run.git_sha_short }}" },
		},
		{
			name: "run.id and run.action anywhere",
			mutate: func(n *ComponentInput) {
				n.Config[helmConfigValues] = "id: ${{ run.id }}"
				n.TargetNamespace = "ns-${{ run.action }}"
			},
		},
		{
			name:   "run.git_ref with an explicit git_ref",
			mutate: func(n *ComponentInput) { n.Config[helmConfigValues] = "ref: ${{ run.git_ref }}" },
		},
		{
			name:   "escaped literal is not a reference",
			mutate: func(n *ComponentInput) { n.Config[helmConfigValues] = "doc: $${{ anything.goes }}" },
		},
		{
			name:    "malformed reference",
			mutate:  func(n *ComponentInput) { n.Config[helmConfigValues] = "a: ${{ vars.X" },
			wantErr: "unterminated",
		},
		{
			name:    "malformed namespace field",
			mutate:  func(n *ComponentInput) { n.TargetNamespace = "ns-${{ vars.X" },
			wantErr: "unterminated",
		},
		{
			name:    "unknown namespace",
			mutate:  func(n *ComponentInput) { n.Config[helmConfigValues] = "a: ${{ env.HOME }}" },
			wantErr: "unknown namespace",
		},
		{
			name:    "unknown run key",
			mutate:  func(n *ComponentInput) { n.Config[helmConfigValues] = "a: ${{ run.bogus }}" },
			wantErr: "unknown run key",
		},
		{
			name:    "run.git_sha outside the values",
			mutate:  func(n *ComponentInput) { n.TargetNamespace = "${{ run.git_sha }}" },
			wantErr: "only available in the inline values",
		},
		{
			name: "run.git_sha on a non-git source",
			mutate: func(n *ComponentInput) {
				n.Config[helmConfigChartSource] = helm.SourceOCI
				n.Config[helm.ConfigRepoURL] = "oci://example.com/charts/app"
				n.Config[helmConfigValues] = "tag: ${{ run.git_sha_short }}"
			},
			wantErr: "requires a git chart source",
		},
		{
			name: "run.git_ref without an explicit git_ref",
			mutate: func(n *ComponentInput) {
				delete(n.Config, helm.ConfigGitRef)
				n.Config[helmConfigValues] = "ref: ${{ run.git_ref }}"
			},
			wantErr: "explicit git_ref",
		},
		{
			// The cross-node rules live in TestValidateDAG_OutputRefs; this
			// asserts a lone node's dangling reference is caught through the
			// same validateDAG entry point.
			name:    "components ref to a component that does not exist",
			mutate:  func(n *ComponentInput) { n.Config[helmConfigValues] = "ns: ${{ components.infra.outputs.namespace }}" },
			wantErr: `no component named "infra"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := base()
			tt.mutate(&n)
			err := validateOneStage([]ComponentInput{n})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected pass, got %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidateDAG_OutputRefs: the cross-node rules for a
// ${{ components.<name>.outputs.<key> }} reference — the named component must
// exist, unambiguously, be an OpenTofu component, and be a transitive
// depends_on ancestor of the referencing node (including through a group).
func TestValidateWorkflow_OutputRefs(t *testing.T) {
	const ref = "${{ components.infra.outputs.namespace }}"
	// tf builds a named terraform component; refNode a helm component
	// referencing components.infra in its inline values.
	tf := func(id uuid.UUID, name string) ComponentInput {
		n := tfNode(nil)
		n.ID = id
		n.Name = name
		return n
	}
	refNode := func(id uuid.UUID) ComponentInput {
		n := helmNode(id)
		n.Name = "web"
		n.Config[helmConfigValues] = "ns: " + ref
		return n
	}
	infra, mid, web := uuid.New(), uuid.New(), uuid.New()
	one := func(c ComponentInput) []ComponentInput { return []ComponentInput{c} }

	t.Run("previous stage passes", func(t *testing.T) {
		if err := validateWorkflow(stagesOf(one(tf(infra, "infra")), one(refNode(web)))); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("any earlier stage passes", func(t *testing.T) {
		stages := stagesOf(one(tf(infra, "infra")), one(helmNode(mid)), one(refNode(web)))
		if err := validateWorkflow(stages); err != nil {
			t.Fatalf("expected an earlier stage to pass, got %v", err)
		}
	})

	t.Run("an empty stage in between passes", func(t *testing.T) {
		stages := stagesOf(one(tf(infra, "infra")), nil, one(refNode(web)))
		if err := validateWorkflow(stages); err != nil {
			t.Fatalf("expected pass across an empty stage, got %v", err)
		}
	})

	t.Run("unknown component name", func(t *testing.T) {
		err := validateWorkflow(stagesOf(one(tf(infra, "database")), one(refNode(web))))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), `no component named "infra"`) {
			t.Fatalf("expected the unknown name rejected, got %v", err)
		}
	})

	t.Run("ambiguous component name", func(t *testing.T) {
		other := uuid.New()
		err := validateWorkflow(stagesOf([]ComponentInput{tf(infra, "infra"), tf(other, "infra")}, one(refNode(web))))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("expected the duplicated referenced name rejected, got %v", err)
		}
	})

	t.Run("non-terraform component", func(t *testing.T) {
		named := helmNode(infra)
		named.Name = "infra"
		err := validateWorkflow(stagesOf(one(named), one(refNode(web))))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "only OpenTofu") {
			t.Fatalf("expected the helm target rejected, got %v", err)
		}
	})

	t.Run("same stage is rejected", func(t *testing.T) {
		// Components of one stage run in parallel, so infra's outputs don't
		// exist yet when web plans.
		err := validateOneStage([]ComponentInput{tf(infra, "infra"), refNode(web)})
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "earlier stage") {
			t.Fatalf("expected the same-stage reference rejected, got %v", err)
		}
	})

	t.Run("later stage is rejected", func(t *testing.T) {
		err := validateWorkflow(stagesOf(one(refNode(web)), one(tf(infra, "infra"))))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "earlier stage") {
			t.Fatalf("expected the later-stage reference rejected, got %v", err)
		}
	})

	t.Run("namespace and release name are checked too", func(t *testing.T) {
		for _, mutate := range []func(*ComponentInput){
			func(n *ComponentInput) { n.TargetNamespace = ref },
			func(n *ComponentInput) { n.Config[helmConfigReleaseName] = "web-" + ref },
		} {
			n := refNode(web)
			n.Config[helmConfigValues] = ""
			mutate(&n)
			err := validateOneStage([]ComponentInput{tf(infra, "infra"), n})
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "earlier stage") {
				t.Fatalf("expected the same-stage ref rejected, got %v", err)
			}
		}
	})
}

func TestValidateConfig_Manifest(t *testing.T) {
	target := uuid.New()
	ok := ComponentInput{
		ID:              uuid.New(),
		Name:            "m",
		Type:            TypeManifest,
		TargetClusterID: &target,
		Config: map[string]string{
			helm.ConfigRepoURL: "https://github.com/acme/manifests",
			manifestConfigPath: "deploy/",
		},
	}
	if err := validateOneStage([]ComponentInput{ok}); err != nil {
		t.Fatalf("expected valid manifest node to pass, got %v", err)
	}

	missingPath := ComponentInput{
		ID:              uuid.New(),
		Name:            "m",
		Type:            TypeManifest,
		TargetClusterID: &target,
		Config:          map[string]string{helm.ConfigRepoURL: "https://github.com/acme/manifests"},
	}
	if err := validateOneStage([]ComponentInput{missingPath}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for missing path, got %v", err)
	}

	missingRepo := ComponentInput{
		ID:              uuid.New(),
		Name:            "m",
		Type:            TypeManifest,
		TargetClusterID: &target,
		Config:          map[string]string{manifestConfigPath: "deploy/"},
	}
	if err := validateOneStage([]ComponentInput{missingRepo}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for missing repo_url, got %v", err)
	}
}

// TestValidateConfig_TargetRules covers the per-type targeting rules: helm needs
// a target cluster + namespace, manifest needs a cluster, and terraform must not
// carry a target.
func TestValidateConfig_TargetRules(t *testing.T) {
	target := uuid.New()
	helmCfg := map[string]string{helmConfigChartSource: helm.SourceOCI, helm.ConfigRepoURL: "oci://x/y"}

	// Helm without a target cluster → rejected.
	noCluster := ComponentInput{ID: uuid.New(), Name: "h", Type: TypeHelm, TargetNamespace: "ns", Config: helmCfg}
	if err := validateOneStage([]ComponentInput{noCluster}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("helm without a target cluster: expected ErrInvalidConfig, got %v", err)
	}
	// Helm without a target namespace → rejected.
	noNS := ComponentInput{ID: uuid.New(), Name: "h", Type: TypeHelm, TargetClusterID: &target, Config: helmCfg}
	if err := validateOneStage([]ComponentInput{noNS}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("helm without a target namespace: expected ErrInvalidConfig, got %v", err)
	}
	// Manifest without a target cluster → rejected.
	mfNoCluster := ComponentInput{ID: uuid.New(), Name: "m", Type: TypeManifest, Config: map[string]string{helm.ConfigRepoURL: "https://github.com/acme/manifests", manifestConfigPath: "deploy/"}}
	if err := validateOneStage([]ComponentInput{mfNoCluster}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("manifest without a target cluster: expected ErrInvalidConfig, got %v", err)
	}
	// Terraform carrying a target cluster → rejected.
	tfTarget := tfNode(nil)
	tfTarget.TargetClusterID = &target
	if err := validateOneStage([]ComponentInput{tfTarget}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("terraform with a target cluster: expected ErrInvalidConfig, got %v", err)
	}
	// Terraform carrying a target namespace → rejected.
	tfNS := tfNode(nil)
	tfNS.TargetNamespace = "ns"
	if err := validateOneStage([]ComponentInput{tfNS}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("terraform with a target namespace: expected ErrInvalidConfig, got %v", err)
	}
}

// TestValidateTerraformBackend proves the state backend must be the supported
// s3 type with its required settings — an unsupported (or absent) backend or a
// missing required setting is rejected at write time.
func TestValidateTerraformBackend(t *testing.T) {
	// Full s3 config (the tfNode default) → valid.
	if err := validateOneStage([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("s3 backend with full config: unexpected error %v", err)
	}
	// No working path → valid (the module is at the repo root); no repo → rejected.
	noPath := tfNode(nil)
	delete(noPath.Config, manifestConfigPath)
	if err := validateOneStage([]ComponentInput{noPath}); err != nil {
		t.Errorf("no working path: unexpected error %v", err)
	}
	noRepo := tfNode(nil)
	delete(noRepo.Config, helm.ConfigRepoURL)
	if err := validateOneStage([]ComponentInput{noRepo}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no repo_url: expected ErrInvalidConfig, got %v", err)
	}
	// No backend → rejected.
	noBackend := tfNode(nil)
	delete(noBackend.Config, terraformConfigBackend)
	if err := validateOneStage([]ComponentInput{noBackend}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no backend: expected ErrInvalidConfig, got %v", err)
	}
	// Unsupported backend types (including the old kubernetes default) → rejected.
	for _, backend := range []string{"kubernetes", "azure", "pg"} {
		n := tfNode(map[string]string{terraformConfigBackend: backend})
		if err := validateOneStage([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("backend=%q: expected ErrInvalidConfig, got %v", backend, err)
		}
	}
	// A missing required s3 setting → rejected.
	for _, key := range []string{"bucket", "key", "region"} {
		n := tfNode(nil)
		cfg := map[string]string{"bucket": "my-state", "key": "prod/terraform.tfstate", "region": "us-east-1"}
		delete(cfg, key)
		b, _ := json.Marshal(cfg)
		n.Config[terraformConfigBackendConfig] = string(b)
		if err := validateOneStage([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("s3 config without %q: expected ErrInvalidConfig, got %v", key, err)
		}
	}
	// Absent backend_config entirely → rejected (the required settings are missing).
	noCfg := tfNode(nil)
	delete(noCfg.Config, terraformConfigBackendConfig)
	if err := validateOneStage([]ComponentInput{noCfg}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no backend_config: expected ErrInvalidConfig, got %v", err)
	}
	// Malformed backend_config JSON → rejected.
	bad := tfNode(map[string]string{terraformConfigBackendConfig: `[not json`})
	if err := validateOneStage([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("malformed backend_config: expected ErrInvalidConfig, got %v", err)
	}
	// Optional s3 settings are accepted alongside the required ones.
	extra := tfNode(map[string]string{terraformConfigBackendConfig: `{"bucket":"my-state","key":"prod/terraform.tfstate","region":"us-east-1","dynamodb_table":"tf-locks","encrypt":"true","kms_key_id":"arn:aws:kms:us-east-1:1:key/k"}`})
	if err := validateOneStage([]ComponentInput{extra}); err != nil {
		t.Errorf("optional s3 settings: unexpected error %v", err)
	}

	// gcs and azurerm: each with its own required settings.
	perBackend := map[string]map[string]string{
		tofu.BackendGCS:   {"bucket": "acme-state", "prefix": "envs/prod"},
		tofu.BackendAzure: {"storage_account_name": "acmestate", "container_name": "tfstate", "key": "prod.tfstate"},
	}
	for backend, full := range perBackend {
		b, _ := json.Marshal(full)
		ok := tfNode(map[string]string{terraformConfigBackend: backend, terraformConfigBackendConfig: string(b)})
		if err := validateOneStage([]ComponentInput{ok}); err != nil {
			t.Errorf("%s backend with full config: unexpected error %v", backend, err)
		}
		for key := range full {
			cfg := map[string]string{}
			for k, v := range full {
				cfg[k] = v
			}
			delete(cfg, key)
			b, _ := json.Marshal(cfg)
			n := tfNode(map[string]string{terraformConfigBackend: backend, terraformConfigBackendConfig: string(b)})
			if err := validateOneStage([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("%s config without %q: expected ErrInvalidConfig, got %v", backend, key, err)
			}
		}
	}
	// use_lockfile is an s3 setting; on another backend it simply passes
	// through (no version gate).
	gcsLock := tfNode(map[string]string{terraformConfigBackend: tofu.BackendGCS, terraformConfigBackendConfig: `{"bucket":"b","prefix":"p","use_lockfile":"true"}`, terraformConfigVersion: "1.9"})
	if err := validateOneStage([]ComponentInput{gcsLock}); err != nil {
		t.Errorf("gcs with use_lockfile on 1.9: unexpected error %v", err)
	}
}

func TestValidateConfig_UnknownType(t *testing.T) {
	n := ComponentInput{ID: uuid.New(), Name: "x", Type: "bogus"}
	if err := validateOneStage([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for unknown type, got %v", err)
	}
}

// tfNode builds a minimal valid terraform node, overlaying any extra config
// keys, so a test can focus on the key under test. It carries the s3 state
// backend with its required settings. command is not authored — it's
// synthesized per run.
func tfNode(extra map[string]string) ComponentInput {
	cfg := map[string]string{
		helm.ConfigRepoURL:           "https://github.com/acme/infra",
		manifestConfigPath:           "envs/prod",
		terraformConfigBackend:       "s3",
		terraformConfigBackendConfig: `{"bucket":"my-state","key":"prod/terraform.tfstate","region":"us-east-1"}`,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return ComponentInput{ID: uuid.New(), Name: "tf", Type: TypeTerraform, Config: cfg}
}

func TestValidateTerraformConfig_CloudCredential(t *testing.T) {
	t.Parallel()

	// No cloud credential is valid (an instance/IRSA role may authenticate the
	// backend and providers).
	if err := validateOneStage([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("no credential: unexpected error %v", err)
	}

	// Valid cloud_credential_id UUID.
	goodCred := tfNode(map[string]string{terraformConfigCloudCredentialID: uuid.New().String()})
	if err := validateOneStage([]ComponentInput{goodCred}); err != nil {
		t.Errorf("valid cloud_credential_id: unexpected error %v", err)
	}

	// Non-UUID cloud_credential_id → ErrInvalidConfig.
	badCred := tfNode(map[string]string{terraformConfigCloudCredentialID: "not-a-uuid"})
	if err := validateOneStage([]ComponentInput{badCred}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad cloud_credential_id: expected ErrInvalidConfig, got %v", err)
	}
}

func TestValidateTerraformConfig_AuthCluster(t *testing.T) {
	t.Parallel()

	// No cluster authentication is valid (the module may not touch Kubernetes).
	if err := validateOneStage([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("no auth cluster: unexpected error %v", err)
	}

	// Valid auth_cluster_id UUID.
	good := tfNode(map[string]string{terraformConfigAuthClusterID: uuid.New().String()})
	if err := validateOneStage([]ComponentInput{good}); err != nil {
		t.Errorf("valid auth_cluster_id: unexpected error %v", err)
	}

	// Non-UUID auth_cluster_id → ErrInvalidConfig.
	bad := tfNode(map[string]string{terraformConfigAuthClusterID: "not-a-uuid"})
	if err := validateOneStage([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad auth_cluster_id: expected ErrInvalidConfig, got %v", err)
	}
}

func TestValidateTerraformConfig_Flags(t *testing.T) {
	t.Parallel()

	for _, key := range []string{terraformConfigInitFlags, terraformConfigPlanFlags, terraformConfigApplyFlags} {
		// A well-formed JSON string array is accepted.
		good := tfNode(map[string]string{key: `["-var=env=prod","-target=aws_instance.web"]`})
		if err := validateOneStage([]ComponentInput{good}); err != nil {
			t.Errorf("%s valid array: unexpected error %v", key, err)
		}
		// A non-array JSON (object) → ErrInvalidConfig.
		obj := tfNode(map[string]string{key: `{"-var":"env=prod"}`})
		if err := validateOneStage([]ComponentInput{obj}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s object: expected ErrInvalidConfig, got %v", key, err)
		}
		// Malformed JSON → ErrInvalidConfig.
		bad := tfNode(map[string]string{key: `[not json`})
		if err := validateOneStage([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s malformed: expected ErrInvalidConfig, got %v", key, err)
		}
	}
}

func TestValidateTerraformConfig_TofuVersion(t *testing.T) {
	t.Parallel()

	// Absent tofu_version is valid (the default line applies).
	if err := validateOneStage([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("no tofu_version: unexpected error %v", err)
	}

	// Every supported line is accepted.
	for _, v := range tofu.Versions {
		n := tfNode(map[string]string{terraformConfigVersion: v.Minor})
		if err := validateOneStage([]ComponentInput{n}); err != nil {
			t.Errorf("tofu_version %q: unexpected error %v", v.Minor, err)
		}
	}

	// An unsupported line → ErrInvalidConfig naming the supported ones.
	bad := tfNode(map[string]string{terraformConfigVersion: "0.11"})
	err := validateOneStage([]ComponentInput{bad})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("tofu_version 0.11: expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), tofu.DefaultVersion) {
		t.Errorf("error should list the supported lines, got: %v", err)
	}
}

func TestValidateTerraformConfig_UseLockfileNeedsNativeLocking(t *testing.T) {
	t.Parallel()

	withLockfile := `{"bucket":"my-state","key":"prod/terraform.tfstate","region":"us-east-1","use_lockfile":"true"}`

	// use_lockfile on the (pre-1.10) default line → rejected at write time
	// rather than failing `tofu init` mid-run on the unknown argument.
	implicitDefault := tfNode(map[string]string{terraformConfigBackendConfig: withLockfile})
	if err := validateOneStage([]ComponentInput{implicitDefault}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("use_lockfile on default line: expected ErrInvalidConfig, got %v", err)
	}
	explicitOld := tfNode(map[string]string{
		terraformConfigVersion:       "1.9",
		terraformConfigBackendConfig: withLockfile,
	})
	if err := validateOneStage([]ComponentInput{explicitOld}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("use_lockfile on 1.9: expected ErrInvalidConfig, got %v", err)
	}

	// On a native-locking line it's accepted — including an explicit opt-out,
	// which is the API author's escape hatch from the automatic injection.
	for _, raw := range []string{withLockfile, strings.ReplaceAll(withLockfile, `"true"`, `"false"`)} {
		n := tfNode(map[string]string{
			terraformConfigVersion:       "1.12",
			terraformConfigBackendConfig: raw,
		})
		if err := validateOneStage([]ComponentInput{n}); err != nil {
			t.Errorf("use_lockfile on 1.12 (%s): unexpected error %v", raw, err)
		}
	}
}

// TestValidateTerraformConfig_WorkspaceAndTFVars: an optional workspace must
// be a safe token (it is shell-quoted into the script and becomes part of
// the state key), and expose_tf_vars must be a boolean string.
func TestValidateTerraformConfig_WorkspaceAndTFVars(t *testing.T) {
	t.Parallel()
	node := func(extra map[string]string) ComponentInput {
		cfg := map[string]string{
			"repo_url": "https://github.com/org/infra.git", "path": ".",
			terraformConfigBackend:       tofu.BackendS3,
			terraformConfigBackendConfig: `{"bucket":"b","key":"k","region":"r"}`,
		}
		for k, v := range extra {
			cfg[k] = v
		}
		return ComponentInput{ID: uuid.New(), Name: "infra", Type: TypeTerraform, Config: cfg}
	}
	for _, ws := range []string{"", "prod", "team-a_v1.2"} {
		if err := validateTerraformConfig(node(map[string]string{terraformConfigWorkspace: ws})); err != nil {
			t.Errorf("workspace %q: unexpected error %v", ws, err)
		}
	}
	for _, ws := range []string{"pro d", "a/b", "$(x)", "env:/x", strings.Repeat("a", 91)} {
		if err := validateTerraformConfig(node(map[string]string{terraformConfigWorkspace: ws})); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("workspace %q: err = %v, want ErrInvalidConfig", ws, err)
		}
	}
	for _, v := range []string{"", "true", "false"} {
		if err := validateTerraformConfig(node(map[string]string{terraformConfigExposeTFVars: v})); err != nil {
			t.Errorf("expose_tf_vars %q: unexpected error %v", v, err)
		}
	}
	if err := validateTerraformConfig(node(map[string]string{terraformConfigExposeTFVars: "yes"})); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expose_tf_vars yes: err = %v, want ErrInvalidConfig", err)
	}
	// Typed inputs: any JSON object keyed by identifiers; not an object, or
	// a key no variable block could carry, is rejected.
	for _, v := range []string{"", " \n", "{}", `{"region":"eu-west-1","replicas":3,"tags":{"a":"b"},"cidrs":["10.0.0.0/8"],"_x-y":null}`} {
		if err := validateTerraformConfig(node(map[string]string{terraformConfigTFVars: v})); err != nil {
			t.Errorf("tfvars %q: unexpected error %v", v, err)
		}
	}
	for _, v := range []string{"null", "[]", `"x"`, "{", `{"1st":"x"}`, `{"a b":"x"}`, `{"":"x"}`} {
		if err := validateTerraformConfig(node(map[string]string{terraformConfigTFVars: v})); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("tfvars %q: err = %v, want ErrInvalidConfig", v, err)
		}
	}
}

// TestValidateTerraformConfig_BackendSecrets: a secret-bearing backend
// setting (an S3 access key, a GCS customer-supplied encryption key, an
// Azure account key) is refused with a pointer to the sensitive Variable the
// backend reads instead — the backend_config object lands in the run's
// script. Key *names* (kms_key_id, kms_encryption_key) stay allowed.
func TestValidateTerraformConfig_BackendSecrets(t *testing.T) {
	t.Parallel()
	node := func(backend, backendCfg string) ComponentInput {
		return ComponentInput{ID: uuid.New(), Name: "infra", Type: TypeTerraform, Config: map[string]string{
			"repo_url": "https://github.com/org/infra.git", "path": ".",
			terraformConfigBackend:       backend,
			terraformConfigBackendConfig: backendCfg,
		}}
	}
	for _, tc := range []struct{ backend, cfg, want string }{
		{tofu.BackendS3, `{"bucket":"b","key":"k","region":"r","secret_key":"x"}`, "AWS_SECRET_ACCESS_KEY"},
		{tofu.BackendS3, `{"bucket":"b","key":"k","region":"r","access_key":""}`, "AWS_ACCESS_KEY_ID"},
		{tofu.BackendGCS, `{"bucket":"b","prefix":"p","encryption_key":"a2V5"}`, "GOOGLE_ENCRYPTION_KEY"},
		{tofu.BackendAzure, `{"storage_account_name":"s","container_name":"c","key":"k","access_key":"x"}`, "ARM_ACCESS_KEY"},
		{tofu.BackendAzure, `{"storage_account_name":"s","container_name":"c","key":"k","sas_token":"x"}`, "ARM_SAS_TOKEN"},
	} {
		err := validateTerraformConfig(node(tc.backend, tc.cfg))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: err = %v, want ErrInvalidConfig naming %s", tc.backend, tc.cfg, err, tc.want)
		}
	}
	for _, tc := range []struct{ backend, cfg string }{
		{tofu.BackendS3, `{"bucket":"b","key":"k","region":"r","kms_key_id":"arn:aws:kms:...","encrypt":"true"}`},
		{tofu.BackendGCS, `{"bucket":"b","prefix":"p","kms_encryption_key":"projects/p/locations/l/keyRings/r/cryptoKeys/k"}`},
		{tofu.BackendAzure, `{"storage_account_name":"s","container_name":"c","key":"k","use_azuread_auth":"true"}`},
	} {
		if err := validateTerraformConfig(node(tc.backend, tc.cfg)); err != nil {
			t.Errorf("%s %s: unexpected error %v", tc.backend, tc.cfg, err)
		}
	}
}

// TestValidateTerraformConfig_ManagedState: the spacefleet backend needs no
// settings and refuses any (Spacefleet owns them all), and refuses the two
// workspace names a URL path treats specially.
func TestValidateTerraformConfig_ManagedState(t *testing.T) {
	t.Parallel()
	managed := func(extra map[string]string) ComponentInput {
		n := tfNode(extra)
		n.Config[terraformConfigBackend] = tofu.BackendSpacefleet
		delete(n.Config, terraformConfigBackendConfig)
		for k, v := range extra {
			n.Config[k] = v
		}
		return n
	}
	if err := validateOneStage([]ComponentInput{managed(nil)}); err != nil {
		t.Errorf("managed state with no settings: %v", err)
	}
	if err := validateOneStage([]ComponentInput{managed(map[string]string{terraformConfigBackendConfig: "{}"})}); err != nil {
		t.Errorf("managed state with an empty settings object: %v", err)
	}
	if err := validateOneStage([]ComponentInput{managed(map[string]string{terraformConfigWorkspace: "prod.eu"})}); err != nil {
		t.Errorf("managed state with a workspace: %v", err)
	}
	for name, extra := range map[string]map[string]string{
		"settings":       {terraformConfigBackendConfig: `{"address":"https://elsewhere"}`},
		"workspace '.'":  {terraformConfigWorkspace: "."},
		"workspace '..'": {terraformConfigWorkspace: ".."},
	} {
		if err := validateOneStage([]ComponentInput{managed(extra)}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}
