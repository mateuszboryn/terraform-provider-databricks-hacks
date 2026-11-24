package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/databricks/databricks-sdk-go"
	"github.com/databricks/databricks-sdk-go/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakeAssetLinksServer is an in-memory implementation of the Knowledge Asset Links API.
type fakeAssetLinksServer struct {
	t       *testing.T
	links   map[string]map[string]any
	parents []string
}

func (f *fakeAssetLinksServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && p == "/api/2.1/unity-catalog/current-metastore-assignment":
		_, _ = w.Write([]byte(`{"metastore_id":"ms-1","workspace_id":42}`))
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/asset-links"):
		parent := strings.TrimSuffix(strings.TrimPrefix(p, "/api/2.0/knowledge/"), "/asset-links")
		f.parents = append(f.parents, parent)
		var link map[string]any
		_ = json.NewDecoder(r.Body).Decode(&link)
		if _, ok := link["asset_link"]; ok {
			f.t.Errorf("asset link must be sent as top-level body, got %v", link)
		}
		// server canonicalizes the table name
		link["asset_id"] = strings.ToLower(link["asset_id"].(string))
		link["name"] = parent + "/asset-links/link-1"
		link["create_time"] = "2026-11-23T10:00:00Z"
		f.links[link["name"].(string)] = link
		_ = json.NewEncoder(w).Encode(link)
	case strings.Contains(p, "/asset-links/"):
		name := strings.TrimPrefix(p, "/api/2.0/knowledge/")
		link, ok := f.links[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_code":"NOT_FOUND","message":"link not found"}`))
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(link)
		case http.MethodDelete:
			delete(f.links, name)
			_, _ = w.Write([]byte(`{}`))
		}
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func newTestPageAssetLinkResource(t *testing.T) (*pageAssetLinkResource, *fakeAssetLinksServer) {
	fake := &fakeAssetLinksServer{t: t, links: map[string]map[string]any{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	w, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token", AccountID: "acc-1"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := client.New(w.Config)
	if err != nil {
		t.Fatal(err)
	}
	return &pageAssetLinkResource{api: api, w: w}, fake
}

func TestPageAssetLinkResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	r, fake := newTestPageAssetLinkResource(t)
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	sch := schemaResp.Schema
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	}

	// Create with asset_scope_id omitted -> metastore ID is looked up
	plan := tfsdk.Plan{Schema: sch}
	model := pageAssetLinkModel{
		ID: types.StringUnknown(), Name: types.StringUnknown(),
		Page: types.StringValue("pages/abc-123"), Category: types.StringValue("RELATED_ASSETS"),
		AssetType: types.StringValue("ASSET_TYPE_UC_TABLE"), AssetScopeID: types.StringUnknown(),
		AssetID: types.StringValue("Main.Sales.Orders"), ResolvedAssetID: types.StringUnknown(), CreateTime: types.StringUnknown(),
	}
	if d := plan.Set(ctx, &model); d.HasError() {
		t.Fatal(d)
	}
	createResp := resource.CreateResponse{State: emptyState()}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatal(createResp.Diagnostics)
	}
	if fake.parents[0] != "pages/abc-123/asset-link-categories/RELATED_ASSETS" {
		t.Fatalf("unexpected parent %q", fake.parents[0])
	}
	const linkName = "pages/abc-123/asset-link-categories/RELATED_ASSETS/asset-links/link-1"
	if sent := fake.links[linkName]["asset_scope_id"]; sent != "ms-1" {
		t.Fatalf("expected metastore scope, got %v", sent)
	}
	var created pageAssetLinkModel
	createResp.State.Get(ctx, &created)
	if created.ID.ValueString() != linkName || created.AssetScopeID.ValueString() != "ms-1" ||
		created.ResolvedAssetID.ValueString() != "main.sales.orders" || created.AssetID.ValueString() != "Main.Sales.Orders" ||
		created.CreateTime.ValueString() == "" {
		t.Fatalf("unexpected created state: %+v", created)
	}

	// Read keeps the configured asset_id despite server normalization
	readResp := resource.ReadResponse{State: createResp.State}
	r.Read(ctx, resource.ReadRequest{State: createResp.State}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatal(readResp.Diagnostics)
	}
	var read pageAssetLinkModel
	readResp.State.Get(ctx, &read)
	if read != created {
		t.Fatalf("read drifted:\n got %+v\nwant %+v", read, created)
	}

	// Import + read populates every attribute
	importResp := resource.ImportStateResponse{State: emptyState()}
	r.ImportState(ctx, resource.ImportStateRequest{ID: linkName}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatal(importResp.Diagnostics)
	}
	readResp = resource.ReadResponse{State: importResp.State}
	r.Read(ctx, resource.ReadRequest{State: importResp.State}, &readResp)
	readResp.State.Get(ctx, &read)
	if read.Page.ValueString() != "pages/abc-123" || read.Category.ValueString() != "RELATED_ASSETS" ||
		read.AssetType.ValueString() != "ASSET_TYPE_UC_TABLE" || read.AssetID.ValueString() != "main.sales.orders" ||
		read.AssetScopeID.ValueString() != "ms-1" {
		t.Fatalf("unexpected imported state: %+v", read)
	}

	// Delete (twice: NOT_FOUND is ignored), then Read removes the resource from state
	for i := 0; i < 2; i++ {
		deleteResp := resource.DeleteResponse{State: readResp.State}
		r.Delete(ctx, resource.DeleteRequest{State: readResp.State}, &deleteResp)
		if deleteResp.Diagnostics.HasError() {
			t.Fatal(deleteResp.Diagnostics)
		}
	}
	readResp = resource.ReadResponse{State: readResp.State}
	r.Read(ctx, resource.ReadRequest{State: readResp.State}, &readResp)
	if readResp.Diagnostics.HasError() || !readResp.State.Raw.IsNull() {
		t.Fatalf("expected resource removed after delete, diags %v", readResp.Diagnostics)
	}
}

func TestPageAssetLinkDefaultScope(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestPageAssetLinkResource(t)
	for assetType, want := range map[string]string{
		"ASSET_TYPE_UC_TABLE":     "ms-1",
		"ASSET_TYPE_EXTERNAL_URL": "acc-1",
		"ASSET_TYPE_ACCOUNT_PAGE": "acc-1",
	} {
		got, err := r.defaultAssetScopeID(ctx, assetType)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", assetType, got, err, want)
		}
	}
	r.w.Config.AccountID = ""
	if _, err := r.defaultAssetScopeID(ctx, "ASSET_TYPE_EXTERNAL_URL"); err == nil {
		t.Error("expected error without account ID")
	}
}

func TestPageAssetLinkImportIDValidation(t *testing.T) {
	for id, wantOK := range map[string]bool{
		"pages/p1/asset-link-categories/SOURCE/asset-links/l1":         true,
		"pages/p1/asset-link-categories/RELATED_ASSETS/asset-links/l1": true,
		"pages/p1/asset-link-categories/OTHER/asset-links/l1":          false,
		"pages/p1/asset-links/l1":                                      false,
		"l1":                                                           false,
		"":                                                             false,
	} {
		if _, _, ok := parseAssetLinkName(id); ok != wantOK {
			t.Errorf("parse %q: ok=%t, want %t", id, ok, wantOK)
		}
	}
}
