package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// GetApplicationComponentOutputs returns, per component, the output keys known
// from each component's latest successful run — the data behind the workflow
// editor's ${{ components.<name>.outputs.<key> }} autocomplete. Editor or above:
// even output key names lean on knowing the deployment's shape, so it's gated
// the same as seeing secret-bearing component config (canSeeSecrets). Keys only,
// never values — the response schema has no value field at all. A missing
// application is a 404.
func (s *Server) GetApplicationComponentOutputs(ctx context.Context, req GetApplicationComponentOutputsRequestObject) (GetApplicationComponentOutputsResponseObject, error) {
	orgID, canSeeSecrets, aerr, err := s.resolveWorkflowRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[GetApplicationComponentOutputsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if !canSeeSecrets {
		return errResp[GetApplicationComponentOutputsdefaultJSONResponse](http.StatusForbidden, "forbidden", "editor access required"), nil
	}
	byComponent, err := s.workflows.LatestOutputKeys(ctx, orgID, req.Id)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[GetApplicationComponentOutputsdefaultJSONResponse](http.StatusNotFound, "not_found", "application not found"), nil
		}
		return nil, err
	}
	return GetApplicationComponentOutputs200JSONResponse(toAPIComponentOutputKeys(byComponent)), nil
}

// toAPIComponentOutputKeys maps the service's per-component output keys to the
// API map (component id string → keys). There is nothing to redact: the schema
// carries no value, only the key, its sensitivity flag, and a type hint.
func toAPIComponentOutputKeys(in map[uuid.UUID][]workflows.OutputKey) ComponentOutputKeys {
	out := make(ComponentOutputKeys, len(in))
	for id, keys := range in {
		mapped := make([]ComponentOutputKey, len(keys))
		for i, k := range keys {
			mapped[i] = ComponentOutputKey{Key: k.Key, Sensitive: k.Sensitive}
			if k.Type != "" {
				t := k.Type
				mapped[i].Type = &t
			}
		}
		out[id.String()] = mapped
	}
	return out
}
