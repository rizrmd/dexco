package builtin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openai/codex/dexco/internal/model"
	permissionstore "github.com/openai/codex/dexco/internal/permissions"
)

func TestRequestPermissionsHandlerRecordsOnlyGrantedRequestedSubset(t *testing.T) {
	t.Parallel()

	store := permissionstore.NewStore()
	requested := model.PermissionGrant{
		Key:         "workspace-write:reports",
		Description: "write generated reports",
	}
	unrequested := model.PermissionGrant{
		Key:         "workspace-write:private",
		Description: "write private reports",
	}
	handler := RequestPermissionsHandler{
		Grants: store,
		Responder: func(context.Context, RequestPermissionsArgs) (RequestPermissionsResponse, error) {
			return RequestPermissionsResponse{
				Grants: []model.PermissionGrant{requested, unrequested},
				Scope:  model.PermissionGrantScopeTurn,
			}, nil
		},
	}

	args, err := json.Marshal(RequestPermissionsArgs{
		Grants: []model.PermissionGrant{requested},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	ctx := permissionstore.ContextWithTurnID(context.Background(), "turn-1")
	result, err := handler.Call(ctx, model.ToolCall{
		CallID:    "permissions-call",
		Name:      "request_permissions",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var response RequestPermissionsResponse
	if err := json.Unmarshal([]byte(result.Output), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v; output=%s", err, result.Output)
	}
	want := RequestPermissionsResponse{
		Grants: []model.PermissionGrant{requested},
		Scope:  model.PermissionGrantScopeTurn,
	}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v, want %#v", response, want)
	}
	if !store.Has("turn-1", requested.Key) {
		t.Fatalf("store missing requested grant %q", requested.Key)
	}
	if store.Has("turn-1", unrequested.Key) {
		t.Fatalf("store recorded unrequested grant %q", unrequested.Key)
	}
}

func TestRequestPermissionsHandlerRejectsStrictSessionGrant(t *testing.T) {
	t.Parallel()

	store := permissionstore.NewStore()
	requested := model.PermissionGrant{Key: "workspace-write:reports"}
	handler := RequestPermissionsHandler{
		Grants: store,
		Responder: func(context.Context, RequestPermissionsArgs) (RequestPermissionsResponse, error) {
			return RequestPermissionsResponse{
				Grants:           []model.PermissionGrant{requested},
				Scope:            model.PermissionGrantScopeSession,
				StrictAutoReview: true,
			}, nil
		},
	}

	args, err := json.Marshal(RequestPermissionsArgs{
		Grants: []model.PermissionGrant{requested},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	ctx := permissionstore.ContextWithTurnID(context.Background(), "turn-1")
	result, err := handler.Call(ctx, model.ToolCall{
		CallID:    "permissions-call",
		Name:      "request_permissions",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var response RequestPermissionsResponse
	if err := json.Unmarshal([]byte(result.Output), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v; output=%s", err, result.Output)
	}
	want := RequestPermissionsResponse{
		Scope: model.PermissionGrantScopeTurn,
	}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v, want %#v", response, want)
	}
	if store.Has("turn-1", requested.Key) || store.Has("", requested.Key) {
		t.Fatalf("store recorded strict session grant %q", requested.Key)
	}
}

func TestRequestPermissionsHandlerRecordsStrictTurnGrantByKey(t *testing.T) {
	t.Parallel()

	store := permissionstore.NewStore()
	requested := model.PermissionGrant{Key: "workspace-write:reports"}
	other := model.PermissionGrant{Key: "workspace-write:private"}
	handler := RequestPermissionsHandler{
		Grants: store,
		Responder: func(context.Context, RequestPermissionsArgs) (RequestPermissionsResponse, error) {
			return RequestPermissionsResponse{
				Grants:           []model.PermissionGrant{requested},
				Scope:            model.PermissionGrantScopeTurn,
				StrictAutoReview: true,
			}, nil
		},
	}

	args, err := json.Marshal(RequestPermissionsArgs{
		Grants: []model.PermissionGrant{requested, other},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	ctx := permissionstore.ContextWithTurnID(context.Background(), "turn-1")
	result, err := handler.Call(ctx, model.ToolCall{
		CallID:    "permissions-call",
		Name:      "request_permissions",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var response RequestPermissionsResponse
	if err := json.Unmarshal([]byte(result.Output), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v; output=%s", err, result.Output)
	}
	want := RequestPermissionsResponse{
		Grants:           []model.PermissionGrant{requested},
		Scope:            model.PermissionGrantScopeTurn,
		StrictAutoReview: true,
	}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("response = %#v, want %#v", response, want)
	}
	if !store.Has("turn-1", requested.Key) {
		t.Fatalf("store missing requested grant %q", requested.Key)
	}
	if store.Has("turn-1", other.Key) {
		t.Fatalf("store recorded ungranted key %q", other.Key)
	}
	if !store.StrictAutoReview("turn-1") {
		t.Fatalf("store missing strict-auto-review marker for turn")
	}
	if !store.StrictAutoReviewForGrant("turn-1", requested.Key) {
		t.Fatalf("store missing strict-auto-review marker for grant %q", requested.Key)
	}
	if store.StrictAutoReviewForGrant("turn-1", other.Key) {
		t.Fatalf("store recorded strict-auto-review marker for ungranted key %q", other.Key)
	}
}
