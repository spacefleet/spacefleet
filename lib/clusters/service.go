// Package clusters holds the cluster-registration use cases: registering a
// Kubernetes cluster in an organization, listing/fetching/deleting them, and
// (re-)probing connectivity. It is a thin wrapper over the ent client that also
// owns credential encryption (lib/secrets) and connection probing (lib/k8s).
//
// Credentials are envelope-encrypted before they touch the database and are
// never returned to callers — handlers map *ent.Cluster to an API type that
// omits the encrypted blob.
package clusters

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// Service is a thin wrapper over the ent client plus the credential sealer.
type Service struct {
	ent    *ent.Client
	sealer *secrets.Sealer
}

func NewService(entClient *ent.Client, sealer *secrets.Sealer) *Service {
	return &Service{ent: entClient, sealer: sealer}
}

// ConnectionInput is the resolved, method-agnostic connection detail the
// handler extracts from a request: a display endpoint, the non-secret config
// map, and the raw (unencrypted) credential blob (nil when the method needs
// none, e.g. in_cluster). The service seals Credentials before persisting.
type ConnectionInput struct {
	Endpoint    string
	Config      map[string]string
	Credentials []byte
}

// CreateParams describes a cluster to register.
type CreateParams struct {
	Name   string
	Method k8s.Method
	ConnectionInput
}

// UpdateParams describes a change to a cluster. A nil field is left unchanged;
// a non-nil Connection re-supplies the full connection detail (and re-probes).
type UpdateParams struct {
	Name       *string
	Connection *ConnectionInput
}

// List returns the organization's clusters, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.Cluster, error) {
	return s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID)).
		Order(ent.Asc(cluster.FieldCreatedAt)).
		All(ctx)
}

// Get returns one cluster scoped to the organization, or ent's NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.Cluster, error) {
	return s.ent.Cluster.Query().
		Where(cluster.OrganizationID(orgID), cluster.ID(id)).
		Only(ctx)
}

// Create registers a cluster (sealing any credential), then probes it and
// records the resulting status before returning.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.Cluster, error) {
	create := s.ent.Cluster.Create().
		SetOrganizationID(orgID).
		SetName(p.Name).
		SetConnectionMethod(cluster.ConnectionMethod(p.Method)).
		SetEndpoint(p.Endpoint).
		SetConfig(nonNilConfig(p.Config))
	if len(p.Credentials) > 0 {
		sealed, err := s.sealer.Seal(p.Credentials)
		if err != nil {
			return nil, err
		}
		create.SetEncryptedCredentials(sealed)
	}
	c, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	return s.probe(ctx, c)
}

// Update renames and/or re-supplies a cluster's connection detail, re-probing
// when the connection changes.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.Cluster, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := c.Update()
	if p.Name != nil {
		upd.SetName(*p.Name)
	}
	if p.Connection != nil {
		upd.SetEndpoint(p.Connection.Endpoint).SetConfig(nonNilConfig(p.Connection.Config))
		if len(p.Connection.Credentials) > 0 {
			sealed, err := s.sealer.Seal(p.Connection.Credentials)
			if err != nil {
				return nil, err
			}
			upd.SetEncryptedCredentials(sealed)
		}
	}
	c, err = upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	if p.Connection != nil {
		return s.probe(ctx, c)
	}
	return c, nil
}

// Delete removes a cluster scoped to the organization.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.ent.Cluster.DeleteOne(c).Exec(ctx)
}

// Test re-probes a cluster's connectivity and records the result.
func (s *Service) Test(ctx context.Context, orgID, id uuid.UUID) (*ent.Cluster, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return s.probe(ctx, c)
}

// Nodes lists the live Kubernetes nodes of a cluster scoped to the
// organization. Unlike probe, a connectivity failure is returned to the caller
// (the cluster row's status is not touched) so the handler can surface it.
func (s *Service) Nodes(ctx context.Context, orgID, id uuid.UUID) ([]k8s.Node, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.ListNodes(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// WatchNodes opens a live watch on a cluster's nodes scoped to the
// organization: an initial snapshot plus a channel of subsequent changes. The
// watch runs until ctx is cancelled. Like Nodes, a connectivity failure is
// returned to the caller rather than recorded on the cluster row.
func (s *Service) WatchNodes(ctx context.Context, orgID, id uuid.UUID) (*k8s.NodeStream, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.WatchNodes(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// Namespaces lists the live Kubernetes namespaces of a cluster scoped to the
// organization. Like Nodes, a connectivity failure is returned to the caller
// rather than recorded on the cluster row.
func (s *Service) Namespaces(ctx context.Context, orgID, id uuid.UUID) ([]k8s.Namespace, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.ListNamespaces(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// WatchNamespaces opens a live watch on a cluster's namespaces scoped to the
// organization. Like WatchNodes, a connectivity failure is returned to the
// caller rather than recorded on the cluster row.
func (s *Service) WatchNamespaces(ctx context.Context, orgID, id uuid.UUID) (*k8s.NamespaceStream, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.WatchNamespaces(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// Pods lists the live Kubernetes pods of a cluster scoped to the organization,
// across all namespaces. Like Nodes, a connectivity failure is returned to the
// caller rather than recorded on the cluster row.
func (s *Service) Pods(ctx context.Context, orgID, id uuid.UUID) ([]k8s.Pod, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.ListPods(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// WatchPods opens a live watch on a cluster's pods (across all namespaces)
// scoped to the organization. Like WatchNodes, a connectivity failure is
// returned to the caller rather than recorded on the cluster row.
func (s *Service) WatchPods(ctx context.Context, orgID, id uuid.UUID) (*k8s.PodStream, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.WatchPods(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// PodLogs opens a log stream for one pod of a cluster scoped to the
// organization, returning the raw line-delimited body for the handler to frame.
// Like Pods, a connectivity failure is returned to the caller rather than
// recorded on the cluster row.
func (s *Service) PodLogs(ctx context.Context, orgID, id uuid.UUID, namespace, name string, opts k8s.LogOptions) (io.ReadCloser, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.StreamPodLogs(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	}, namespace, name, opts)
}

// Capabilities resolves the caller identity and the per-capability access the
// cluster's stored credentials grant, scoped to the organization. Like Nodes, a
// connectivity failure is returned to the caller (the cluster row's status is
// not touched) so the handler can surface it.
func (s *Service) Capabilities(ctx context.Context, orgID, id uuid.UUID) (*k8s.CapabilityReport, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return nil, err
	}
	return k8s.Inspect(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// Identity resolves the caller identity the cluster's stored credentials map
// to, scoped to the organization. It is the lightweight counterpart to
// Capabilities — it issues only the identity review, not the per-capability
// access reviews — used to fill the binding subject of a generated RBAC
// manifest. Like Nodes, a failure to build the client or reach the cluster is
// returned to the caller; the cluster row's status is not touched.
func (s *Service) Identity(ctx context.Context, orgID, id uuid.UUID) (k8s.Identity, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return k8s.Identity{}, err
	}
	creds, err := s.openCreds(c)
	if err != nil {
		return k8s.Identity{}, err
	}
	return k8s.ResolveIdentity(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
}

// probe opens the stored credentials, checks reachability, and records the
// status/version/timestamp. A failure to build or reach the cluster is captured
// as an error status on the row rather than returned as a service error — the
// caller still gets the (updated) cluster back.
func (s *Service) probe(ctx context.Context, c *ent.Cluster) (*ent.Cluster, error) {
	creds, err := s.openCreds(c)
	if err != nil {
		return s.record(ctx, c, "", err)
	}
	version, perr := k8s.Probe(ctx, k8s.Connection{
		Method:      k8s.Method(c.ConnectionMethod),
		Endpoint:    c.Endpoint,
		Config:      c.Config,
		Credentials: creds,
	})
	return s.record(ctx, c, version, perr)
}

// openCreds decrypts the stored credential blob, or returns nil when the method
// stores none (in_cluster). A NULL column can surface as a non-nil empty slice,
// so emptiness — not just nil — means "no credentials".
func (s *Service) openCreds(c *ent.Cluster) ([]byte, error) {
	if c.EncryptedCredentials == nil || len(*c.EncryptedCredentials) == 0 {
		return nil, nil
	}
	return s.sealer.Open(*c.EncryptedCredentials)
}

// record writes the outcome of a probe onto the cluster row.
func (s *Service) record(ctx context.Context, c *ent.Cluster, version string, probeErr error) (*ent.Cluster, error) {
	upd := c.Update().SetLastCheckedAt(time.Now())
	if probeErr != nil {
		upd.SetStatus(cluster.StatusError).
			SetStatusMessage(probeErr.Error()).
			SetK8sVersion("")
	} else {
		upd.SetStatus(cluster.StatusConnected).
			SetStatusMessage("").
			SetK8sVersion(version)
	}
	return upd.Save(ctx)
}

// nonNilConfig guards against a nil map reaching the JSON column.
func nonNilConfig(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
