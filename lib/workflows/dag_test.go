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

// helmNode builds a minimal valid helm node with the given id and dependencies,
// so a test can focus on graph shape rather than config. A helm node carries a
// target cluster + namespace (both required for the type).
func helmNode(id uuid.UUID, deps ...uuid.UUID) ComponentInput {
	target := uuid.New()
	return ComponentInput{
		ID:              id,
		Name:            "n-" + id.String()[:8],
		Type:            TypeHelm,
		DependsOn:       deps,
		TargetClusterID: &target,
		TargetNamespace: "ns",
		Config: map[string]string{
			helmConfigChartSource: helm.SourceOCI,
			helm.ConfigRepoURL:    "oci://example.com/charts/app",
		},
	}
}

func TestValidateDAG_Acyclic(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []ComponentInput{
		helmNode(a),
		helmNode(b, a),
		helmNode(c, b),
	}
	if err := validateDAG(nodes); err != nil {
		t.Fatalf("expected acyclic chain to pass, got %v", err)
	}
}

func TestValidateDAG_RejectsNonSlugName(t *testing.T) {
	// A component name must be a slug — it is referenced by name in output
	// expressions and seeds the default release name.
	for _, name := range []string{"My App", "web_1", "-web", ""} {
		n := helmNode(uuid.New())
		n.Name = name
		if err := validateDAG([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("name %q: expected ErrInvalidConfig, got %v", name, err)
		}
	}
	// A slug passes.
	n := helmNode(uuid.New())
	n.Name = "web-1"
	if err := validateDAG([]ComponentInput{n}); err != nil {
		t.Errorf("slug name: expected pass, got %v", err)
	}
}

func TestValidateDAG_Diamond(t *testing.T) {
	// a → {b, c} → d : a valid diamond (b and c both depend on a, d on both).
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []ComponentInput{
		helmNode(a),
		helmNode(b, a),
		helmNode(c, a),
		helmNode(d, b, c),
	}
	if err := validateDAG(nodes); err != nil {
		t.Fatalf("expected diamond to pass, got %v", err)
	}
}

func TestValidateDAG_Empty(t *testing.T) {
	if err := validateDAG(nil); err != nil {
		t.Fatalf("expected empty workflow to pass, got %v", err)
	}
}

func TestValidateDAG_MissingID(t *testing.T) {
	nodes := []ComponentInput{helmNode(uuid.Nil)}
	if err := validateDAG(nodes); !errors.Is(err, ErrMissingID) {
		t.Fatalf("expected ErrMissingID, got %v", err)
	}
}

func TestValidateDAG_DuplicateID(t *testing.T) {
	a := uuid.New()
	nodes := []ComponentInput{helmNode(a), helmNode(a)}
	if err := validateDAG(nodes); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("expected ErrDuplicateID, got %v", err)
	}
}

func TestValidateDAG_SelfDependency(t *testing.T) {
	a := uuid.New()
	nodes := []ComponentInput{helmNode(a, a)}
	if err := validateDAG(nodes); !errors.Is(err, ErrSelfDependency) {
		t.Fatalf("expected ErrSelfDependency, got %v", err)
	}
}

func TestValidateDAG_UnknownDependency(t *testing.T) {
	a := uuid.New()
	missing := uuid.New()
	nodes := []ComponentInput{helmNode(a, missing)}
	if err := validateDAG(nodes); !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("expected ErrUnknownDependency, got %v", err)
	}
}

func TestValidateDAG_SimpleCycle(t *testing.T) {
	// a → b → a.
	a, b := uuid.New(), uuid.New()
	nodes := []ComponentInput{
		helmNode(a, b),
		helmNode(b, a),
	}
	if err := validateDAG(nodes); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
}

func TestValidateDAG_ThreeNodeCycle(t *testing.T) {
	// a → b → c → a.
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []ComponentInput{
		helmNode(a, c),
		helmNode(b, a),
		helmNode(c, b),
	}
	if err := validateDAG(nodes); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
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
			err := validateDAG([]ComponentInput{n})
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
			err := validateDAG([]ComponentInput{n})
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
func TestValidateDAG_OutputRefs(t *testing.T) {
	const ref = "${{ components.infra.outputs.namespace }}"
	// tf builds a named terraform node; refNode a helm node referencing
	// components.infra in its inline values.
	tf := func(id uuid.UUID, name string, deps ...uuid.UUID) ComponentInput {
		n := tfNode(nil)
		n.ID = id
		n.Name = name
		n.DependsOn = deps
		return n
	}
	refNode := func(id uuid.UUID, deps ...uuid.UUID) ComponentInput {
		n := helmNode(id, deps...)
		n.Name = "web"
		n.Config[helmConfigValues] = "ns: " + ref
		return n
	}
	infra, mid, web := uuid.New(), uuid.New(), uuid.New()

	t.Run("direct ancestor passes", func(t *testing.T) {
		if err := validateDAG([]ComponentInput{tf(infra, "infra"), refNode(web, infra)}); err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("transitive ancestor passes", func(t *testing.T) {
		nodes := []ComponentInput{tf(infra, "infra"), helmNode(mid, infra), refNode(web, mid)}
		if err := validateDAG(nodes); err != nil {
			t.Fatalf("expected transitive ancestor to pass, got %v", err)
		}
	})

	t.Run("ancestor through a group passes", func(t *testing.T) {
		// infra is a member of a group the referencing node depends on — the
		// expanded edges (the ones the run executes) make it an ancestor.
		groupID := uuid.New()
		member := tf(infra, "infra")
		member.GroupID = &groupID
		nodes := []ComponentInput{member, refNode(web, groupID)}
		groups := []GroupInput{{ID: groupID, Name: "platform"}}
		if err := validateWorkflow(nodes, groups); err != nil {
			t.Fatalf("expected group-routed ancestor to pass, got %v", err)
		}
	})

	t.Run("unknown component name", func(t *testing.T) {
		err := validateDAG([]ComponentInput{tf(infra, "database"), refNode(web, infra)})
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), `no component named "infra"`) {
			t.Fatalf("expected the unknown name rejected, got %v", err)
		}
	})

	t.Run("ambiguous component name", func(t *testing.T) {
		other := uuid.New()
		err := validateDAG([]ComponentInput{tf(infra, "infra"), tf(other, "infra"), refNode(web, infra, other)})
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("expected the duplicated referenced name rejected, got %v", err)
		}
	})

	t.Run("non-terraform component", func(t *testing.T) {
		named := helmNode(infra)
		named.Name = "infra"
		err := validateDAG([]ComponentInput{named, refNode(web, infra)})
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "only OpenTofu") {
			t.Fatalf("expected the helm target rejected, got %v", err)
		}
	})

	t.Run("not an ancestor", func(t *testing.T) {
		err := validateDAG([]ComponentInput{tf(infra, "infra"), refNode(web)})
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "upstream dependency") {
			t.Fatalf("expected the non-ancestor rejected, got %v", err)
		}
	})

	t.Run("downstream is not an ancestor", func(t *testing.T) {
		// The edge points the wrong way: infra depends on web.
		err := validateDAG([]ComponentInput{tf(infra, "infra", web), refNode(web)})
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "upstream dependency") {
			t.Fatalf("expected the descendant rejected, got %v", err)
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
			err := validateDAG([]ComponentInput{tf(infra, "infra"), n})
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "upstream dependency") {
				t.Fatalf("expected the non-ancestor ref rejected, got %v", err)
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
	if err := validateDAG([]ComponentInput{ok}); err != nil {
		t.Fatalf("expected valid manifest node to pass, got %v", err)
	}

	missingPath := ComponentInput{
		ID:              uuid.New(),
		Name:            "m",
		Type:            TypeManifest,
		TargetClusterID: &target,
		Config:          map[string]string{helm.ConfigRepoURL: "https://github.com/acme/manifests"},
	}
	if err := validateDAG([]ComponentInput{missingPath}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for missing path, got %v", err)
	}

	missingRepo := ComponentInput{
		ID:              uuid.New(),
		Name:            "m",
		Type:            TypeManifest,
		TargetClusterID: &target,
		Config:          map[string]string{manifestConfigPath: "deploy/"},
	}
	if err := validateDAG([]ComponentInput{missingRepo}); !errors.Is(err, ErrInvalidConfig) {
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
	if err := validateDAG([]ComponentInput{noCluster}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("helm without a target cluster: expected ErrInvalidConfig, got %v", err)
	}
	// Helm without a target namespace → rejected.
	noNS := ComponentInput{ID: uuid.New(), Name: "h", Type: TypeHelm, TargetClusterID: &target, Config: helmCfg}
	if err := validateDAG([]ComponentInput{noNS}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("helm without a target namespace: expected ErrInvalidConfig, got %v", err)
	}
	// Manifest without a target cluster → rejected.
	mfNoCluster := ComponentInput{ID: uuid.New(), Name: "m", Type: TypeManifest, Config: map[string]string{helm.ConfigRepoURL: "https://github.com/acme/manifests", manifestConfigPath: "deploy/"}}
	if err := validateDAG([]ComponentInput{mfNoCluster}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("manifest without a target cluster: expected ErrInvalidConfig, got %v", err)
	}
	// Terraform carrying a target cluster → rejected.
	tfTarget := tfNode(nil)
	tfTarget.TargetClusterID = &target
	if err := validateDAG([]ComponentInput{tfTarget}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("terraform with a target cluster: expected ErrInvalidConfig, got %v", err)
	}
	// Terraform carrying a target namespace → rejected.
	tfNS := tfNode(nil)
	tfNS.TargetNamespace = "ns"
	if err := validateDAG([]ComponentInput{tfNS}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("terraform with a target namespace: expected ErrInvalidConfig, got %v", err)
	}
}

// TestValidateTerraformBackend proves the state backend must be the supported
// s3 type with its required settings — an unsupported (or absent) backend or a
// missing required setting is rejected at write time.
func TestValidateTerraformBackend(t *testing.T) {
	// Full s3 config (the tfNode default) → valid.
	if err := validateDAG([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("s3 backend with full config: unexpected error %v", err)
	}
	// No backend → rejected.
	noBackend := tfNode(nil)
	delete(noBackend.Config, terraformConfigBackend)
	if err := validateDAG([]ComponentInput{noBackend}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no backend: expected ErrInvalidConfig, got %v", err)
	}
	// Unsupported backend types (including the old kubernetes default) → rejected.
	for _, backend := range []string{"kubernetes", "gcs", "pg"} {
		n := tfNode(map[string]string{terraformConfigBackend: backend})
		if err := validateDAG([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
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
		if err := validateDAG([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("s3 config without %q: expected ErrInvalidConfig, got %v", key, err)
		}
	}
	// Absent backend_config entirely → rejected (the required settings are missing).
	noCfg := tfNode(nil)
	delete(noCfg.Config, terraformConfigBackendConfig)
	if err := validateDAG([]ComponentInput{noCfg}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no backend_config: expected ErrInvalidConfig, got %v", err)
	}
	// Malformed backend_config JSON → rejected.
	bad := tfNode(map[string]string{terraformConfigBackendConfig: `[not json`})
	if err := validateDAG([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("malformed backend_config: expected ErrInvalidConfig, got %v", err)
	}
	// Optional s3 settings are accepted alongside the required ones.
	extra := tfNode(map[string]string{terraformConfigBackendConfig: `{"bucket":"my-state","key":"prod/terraform.tfstate","region":"us-east-1","dynamodb_table":"tf-locks","encrypt":"true","kms_key_id":"arn:aws:kms:us-east-1:1:key/k"}`})
	if err := validateDAG([]ComponentInput{extra}); err != nil {
		t.Errorf("optional s3 settings: unexpected error %v", err)
	}
}

func TestValidateConfig_UnknownType(t *testing.T) {
	n := ComponentInput{ID: uuid.New(), Name: "x", Type: "bogus"}
	if err := validateDAG([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
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
	if err := validateDAG([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("no credential: unexpected error %v", err)
	}

	// Valid cloud_credential_id UUID.
	goodCred := tfNode(map[string]string{terraformConfigCloudCredentialID: uuid.New().String()})
	if err := validateDAG([]ComponentInput{goodCred}); err != nil {
		t.Errorf("valid cloud_credential_id: unexpected error %v", err)
	}

	// Non-UUID cloud_credential_id → ErrInvalidConfig.
	badCred := tfNode(map[string]string{terraformConfigCloudCredentialID: "not-a-uuid"})
	if err := validateDAG([]ComponentInput{badCred}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad cloud_credential_id: expected ErrInvalidConfig, got %v", err)
	}
}

func TestValidateTerraformConfig_AuthCluster(t *testing.T) {
	t.Parallel()

	// No cluster authentication is valid (the module may not touch Kubernetes).
	if err := validateDAG([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("no auth cluster: unexpected error %v", err)
	}

	// Valid auth_cluster_id UUID.
	good := tfNode(map[string]string{terraformConfigAuthClusterID: uuid.New().String()})
	if err := validateDAG([]ComponentInput{good}); err != nil {
		t.Errorf("valid auth_cluster_id: unexpected error %v", err)
	}

	// Non-UUID auth_cluster_id → ErrInvalidConfig.
	bad := tfNode(map[string]string{terraformConfigAuthClusterID: "not-a-uuid"})
	if err := validateDAG([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad auth_cluster_id: expected ErrInvalidConfig, got %v", err)
	}
}

func TestValidateTerraformConfig_Flags(t *testing.T) {
	t.Parallel()

	for _, key := range []string{terraformConfigInitFlags, terraformConfigPlanFlags, terraformConfigApplyFlags} {
		// A well-formed JSON string array is accepted.
		good := tfNode(map[string]string{key: `["-var=env=prod","-target=aws_instance.web"]`})
		if err := validateDAG([]ComponentInput{good}); err != nil {
			t.Errorf("%s valid array: unexpected error %v", key, err)
		}
		// A non-array JSON (object) → ErrInvalidConfig.
		obj := tfNode(map[string]string{key: `{"-var":"env=prod"}`})
		if err := validateDAG([]ComponentInput{obj}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s object: expected ErrInvalidConfig, got %v", key, err)
		}
		// Malformed JSON → ErrInvalidConfig.
		bad := tfNode(map[string]string{key: `[not json`})
		if err := validateDAG([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s malformed: expected ErrInvalidConfig, got %v", key, err)
		}
	}
}

func TestValidateTerraformConfig_TofuVersion(t *testing.T) {
	t.Parallel()

	// Absent tofu_version is valid (the default line applies).
	if err := validateDAG([]ComponentInput{tfNode(nil)}); err != nil {
		t.Errorf("no tofu_version: unexpected error %v", err)
	}

	// Every supported line is accepted.
	for _, v := range tofu.Versions {
		n := tfNode(map[string]string{terraformConfigVersion: v.Minor})
		if err := validateDAG([]ComponentInput{n}); err != nil {
			t.Errorf("tofu_version %q: unexpected error %v", v.Minor, err)
		}
	}

	// An unsupported line → ErrInvalidConfig naming the supported ones.
	bad := tfNode(map[string]string{terraformConfigVersion: "0.11"})
	err := validateDAG([]ComponentInput{bad})
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
	if err := validateDAG([]ComponentInput{implicitDefault}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("use_lockfile on default line: expected ErrInvalidConfig, got %v", err)
	}
	explicitOld := tfNode(map[string]string{
		terraformConfigVersion:       "1.9",
		terraformConfigBackendConfig: withLockfile,
	})
	if err := validateDAG([]ComponentInput{explicitOld}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("use_lockfile on 1.9: expected ErrInvalidConfig, got %v", err)
	}

	// On a native-locking line it's accepted — including an explicit opt-out,
	// which is the API author's escape hatch from the automatic injection.
	for _, raw := range []string{withLockfile, strings.ReplaceAll(withLockfile, `"true"`, `"false"`)} {
		n := tfNode(map[string]string{
			terraformConfigVersion:       "1.12",
			terraformConfigBackendConfig: raw,
		})
		if err := validateDAG([]ComponentInput{n}); err != nil {
			t.Errorf("use_lockfile on 1.12 (%s): unexpected error %v", raw, err)
		}
	}
}
