package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/databricks/databricks-sdk-go"
	"github.com/databricks/databricks-sdk-go/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakePagesServer is an in-memory implementation of the Knowledge Pages API.
type fakePagesServer struct {
	t     *testing.T
	pages map[string]map[string]any
	masks []string
	reqID string
}

func (f *fakePagesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPost && p == "/api/2.0/knowledge/pages":
		f.reqID = r.URL.Query().Get("request_id")
		var page map[string]any
		_ = json.NewDecoder(r.Body).Decode(&page)
		if _, ok := page["page"]; ok {
			f.t.Errorf("page must be sent as top-level body, got %v", page)
		}
		if page["state"] == nil {
			page["state"] = "DRAFT"
		}
		page["name"] = "pages/abc-123"
		f.pages["pages/abc-123"] = page
		_ = json.NewEncoder(w).Encode(page)
	case strings.HasPrefix(p, "/api/2.0/knowledge/pages/"):
		name := strings.TrimPrefix(p, "/api/2.0/knowledge/")
		page, ok := f.pages[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_code":"NOT_FOUND","message":"page not found"}`))
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(page)
		case http.MethodPatch:
			mask := r.URL.Query().Get("update_mask")
			f.masks = append(f.masks, mask)
			var patch map[string]any
			_ = json.NewDecoder(r.Body).Decode(&patch)
			for _, field := range strings.Split(mask, ",") {
				page[field] = patch[field]
			}
			_ = json.NewEncoder(w).Encode(page)
		case http.MethodDelete:
			delete(f.pages, name)
			_, _ = w.Write([]byte(`{}`))
		}
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func newTestPageResource(t *testing.T) (*pageResource, *fakePagesServer) {
	fake := &fakePagesServer{t: t, pages: map[string]map[string]any{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	w, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := client.New(w.Config)
	if err != nil {
		t.Fatal(err)
	}
	return &pageResource{api: api}, fake
}

func stringSet(values ...string) types.Set {
	elems := make([]attr.Value, len(values))
	for i, v := range values {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func TestPageResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	r, fake := newTestPageResource(t)
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	sch := schemaResp.Schema
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	}

	// Create
	plan := tfsdk.Plan{Schema: sch}
	model := pageModel{
		ID: types.StringUnknown(), Name: types.StringUnknown(),
		Domain: types.StringValue("domains/sales"), DisplayName: types.StringValue("Revenue"),
		Body: types.StringValue("# Revenue"), Synonyms: stringSet("sales", "income"),
		Description: types.StringNull(), State: types.StringValue("DRAFT"),
	}
	if d := plan.Set(ctx, &model); d.HasError() {
		t.Fatal(d)
	}
	createResp := resource.CreateResponse{State: emptyState()}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatal(createResp.Diagnostics)
	}
	if len(fake.reqID) != 36 {
		t.Errorf("expected UUID request_id, got %q", fake.reqID)
	}
	var created pageModel
	createResp.State.Get(ctx, &created)
	if created.ID.ValueString() != "pages/abc-123" || created.Name.ValueString() != "pages/abc-123" {
		t.Fatalf("unexpected id/name: %v %v", created.ID, created.Name)
	}
	if _, sent := fake.pages["pages/abc-123"]["description"]; sent {
		t.Error("null description must not be sent on create")
	}

	// Read (server sorts synonyms) - must not produce differences
	readResp := resource.ReadResponse{State: createResp.State}
	r.Read(ctx, resource.ReadRequest{State: createResp.State}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatal(readResp.Diagnostics)
	}
	var read pageModel
	readResp.State.Get(ctx, &read)
	if !read.Synonyms.Equal(created.Synonyms) || !read.Description.IsNull() || read.Body.ValueString() != "# Revenue" || read.State.ValueString() != "DRAFT" {
		t.Fatalf("read drifted: %+v", read)
	}

	// Update: publish, clear synonyms, set description
	updated := read
	updated.State = types.StringValue("PUBLISHED")
	updated.Synonyms = types.SetNull(types.StringType)
	updated.Description = types.StringValue("Revenue KPIs")
	updatePlan := tfsdk.Plan{Schema: sch}
	updatePlan.Set(ctx, &updated)
	updateResp := resource.UpdateResponse{State: readResp.State}
	r.Update(ctx, resource.UpdateRequest{Plan: updatePlan, State: readResp.State}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatal(updateResp.Diagnostics)
	}
	gotMask := strings.Split(fake.masks[0], ",")
	sort.Strings(gotMask)
	if strings.Join(gotMask, ",") != "description,state,synonyms" {
		t.Fatalf("unexpected update_mask %q", fake.masks[0])
	}
	if syn, ok := fake.pages["pages/abc-123"]["synonyms"].([]any); !ok || len(syn) != 0 {
		t.Fatalf("synonyms must be cleared with an empty list, got %#v", fake.pages["pages/abc-123"]["synonyms"])
	}
	readResp = resource.ReadResponse{State: updateResp.State}
	r.Read(ctx, resource.ReadRequest{State: updateResp.State}, &readResp)
	readResp.State.Get(ctx, &read)
	if read.State.ValueString() != "PUBLISHED" || read.Description.ValueString() != "Revenue KPIs" || !read.Synonyms.IsNull() {
		t.Fatalf("unexpected state after update: %+v", read)
	}

	// Import + read populates every attribute
	importResp := resource.ImportStateResponse{State: emptyState()}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "abc-123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatal(importResp.Diagnostics)
	}
	readResp = resource.ReadResponse{State: importResp.State}
	r.Read(ctx, resource.ReadRequest{State: importResp.State}, &readResp)
	readResp.State.Get(ctx, &read)
	if read.Domain.ValueString() != "domains/sales" || read.DisplayName.ValueString() != "Revenue" || read.ID.ValueString() != "pages/abc-123" {
		t.Fatalf("unexpected imported state: %+v", read)
	}

	// Delete, then Read removes the resource from state
	deleteResp := resource.DeleteResponse{State: readResp.State}
	r.Delete(ctx, resource.DeleteRequest{State: readResp.State}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatal(deleteResp.Diagnostics)
	}
	readResp = resource.ReadResponse{State: readResp.State}
	r.Read(ctx, resource.ReadRequest{State: readResp.State}, &readResp)
	if readResp.Diagnostics.HasError() || !readResp.State.Raw.IsNull() {
		t.Fatalf("expected resource removed after delete, diags %v", readResp.Diagnostics)
	}
}

func TestPageImportIDValidation(t *testing.T) {
	ctx := context.Background()
	r := &pageResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	for id, wantOK := range map[string]bool{"pages/abc": true, "abc": true, "": false, "pages/a/b": false, "domains/x/pages/y": false} {
		state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
		resp := resource.ImportStateResponse{State: state}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() == wantOK {
			t.Errorf("import %q: ok=%t, diags %v", id, wantOK, resp.Diagnostics)
			continue
		}
		if wantOK {
			var got types.String
			resp.State.GetAttribute(ctx, path.Root("id"), &got)
			if !strings.HasPrefix(got.ValueString(), "pages/") {
				t.Errorf("import %q: id %q", id, got.ValueString())
			}
		}
	}
}
