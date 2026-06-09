package workflows

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/lib/helm"
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

// TestValidateTerraformRequiresExplicitBackend proves a terraform node with no
// explicit backend (empty, or the kubernetes default, in managed mode) is
// rejected — it has no injected kubeconfig.
func TestValidateTerraformRequiresExplicitBackend(t *testing.T) {
	base := map[string]string{
		helm.ConfigRepoURL: "https://github.com/acme/infra",
		manifestConfigPath: "envs/prod",
	}
	mk := func(extra map[string]string) ComponentInput {
		cfg := map[string]string{}
		for k, v := range base {
			cfg[k] = v
		}
		for k, v := range extra {
			cfg[k] = v
		}
		return ComponentInput{ID: uuid.New(), Name: "tf", Type: TypeTerraform, Config: cfg}
	}
	// Managed mode, no backend → rejected.
	if err := validateDAG([]ComponentInput{mk(nil)}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("managed mode without a backend: expected ErrInvalidConfig, got %v", err)
	}
	// Managed mode, kubernetes backend → rejected.
	if err := validateDAG([]ComponentInput{mk(map[string]string{terraformConfigBackend: "kubernetes"})}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("managed kubernetes backend: expected ErrInvalidConfig, got %v", err)
	}
	// Managed mode, explicit s3 backend → valid.
	if err := validateDAG([]ComponentInput{mk(map[string]string{terraformConfigBackend: "s3"})}); err != nil {
		t.Errorf("managed s3 backend: unexpected error %v", err)
	}
	// BYO mode (module owns its backend) → valid even with no backend key.
	if err := validateDAG([]ComponentInput{mk(map[string]string{terraformConfigBackendMode: "byo"})}); err != nil {
		t.Errorf("byo mode: unexpected error %v", err)
	}
}

func TestValidateConfig_UnknownType(t *testing.T) {
	n := ComponentInput{ID: uuid.New(), Name: "x", Type: "bogus"}
	if err := validateDAG([]ComponentInput{n}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for unknown type, got %v", err)
	}
}

// tfNode builds a minimal valid terraform node, overlaying any extra config
// keys, so a test can focus on the key under test. It carries an explicit
// (non-kubernetes) backend, required now that terraform gets no injected
// kubeconfig. command is not authored — it's synthesized per run.
func tfNode(extra map[string]string) ComponentInput {
	cfg := map[string]string{
		helm.ConfigRepoURL:     "https://github.com/acme/infra",
		manifestConfigPath:     "envs/prod",
		terraformConfigBackend: "s3",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return ComponentInput{ID: uuid.New(), Name: "tf", Type: TypeTerraform, Config: cfg}
}

func TestValidateTerraformConfig_BackendModeAndCloudCredential(t *testing.T) {
	t.Parallel()

	// Valid backend modes (managed, byo) and absent (defaults managed).
	for _, mode := range []string{"", "managed", "byo"} {
		n := tfNode(map[string]string{terraformConfigBackendMode: mode})
		if err := validateDAG([]ComponentInput{n}); err != nil {
			t.Errorf("backend_mode=%q: unexpected error %v", mode, err)
		}
	}

	// Invalid backend mode → ErrInvalidConfig.
	bad := tfNode(map[string]string{terraformConfigBackendMode: "nonsense"})
	if err := validateDAG([]ComponentInput{bad}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad backend_mode: expected ErrInvalidConfig, got %v", err)
	}

	// byo mode WITHOUT a cloud credential is valid (an instance/IRSA role may
	// authenticate the backend).
	byoNoCred := tfNode(map[string]string{terraformConfigBackendMode: "byo"})
	if err := validateDAG([]ComponentInput{byoNoCred}); err != nil {
		t.Errorf("byo without credential: unexpected error %v", err)
	}

	// Valid cloud_credential_id UUID.
	goodCred := tfNode(map[string]string{
		terraformConfigBackendMode:       "byo",
		terraformConfigCloudCredentialID: uuid.New().String(),
	})
	if err := validateDAG([]ComponentInput{goodCred}); err != nil {
		t.Errorf("valid cloud_credential_id: unexpected error %v", err)
	}

	// Non-UUID cloud_credential_id → ErrInvalidConfig.
	badCred := tfNode(map[string]string{terraformConfigCloudCredentialID: "not-a-uuid"})
	if err := validateDAG([]ComponentInput{badCred}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad cloud_credential_id: expected ErrInvalidConfig, got %v", err)
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
