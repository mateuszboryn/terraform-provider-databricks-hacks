package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/databricks/databricks-sdk-go"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderSchemaDoesNotRequireWarehouse(t *testing.T) {
	var resp provider.SchemaResponse
	(&metricViewProvider{}).Schema(context.Background(), provider.SchemaRequest{}, &resp)
	if _, ok := resp.Schema.Attributes["warehouse_id"]; ok {
		t.Fatal("provider schema must not contain warehouse_id")
	}
	for _, name := range []string{"host", "token", "profile"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("provider schema is missing %q", name)
		}
	}
}

func TestMetricViewSchemaHasSeparateArguments(t *testing.T) {
	var resp resource.SchemaResponse
	NewMetricViewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"warehouse_id", "catalog_name", "schema_name", "name", "definition"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("resource schema is missing %q", name)
		}
	}
	warehouse, ok := resp.Schema.Attributes["warehouse_id"].(resourceschema.StringAttribute)
	if !ok || !warehouse.Required {
		t.Fatal("warehouse_id must be a required resource argument")
	}
	if _, ok := resp.Schema.Attributes["full_name"]; ok {
		t.Fatal("resource schema must use separate catalog, schema, and name arguments")
	}
}

func TestExecSQLUsesRequestedWarehouse(t *testing.T) {
	var warehouseIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/api/2.0/sql/statements") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			WarehouseID string `json:"warehouse_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode statement request: %v", err)
		}
		warehouseIDs = append(warehouseIDs, request.WarehouseID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"statement_id":"statement-id","status":{"state":"SUCCEEDED"}}`))
	}))
	defer server.Close()

	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("create Databricks client without provider-level warehouse: %v", err)
	}
	c := &clientData{w: client}
	for _, warehouseID := range []string{"warehouse-one", "warehouse-two"} {
		if err := c.execSQL(context.Background(), warehouseID, "SELECT 1"); err != nil {
			t.Fatalf("execute statement on %s: %v", warehouseID, err)
		}
	}
	if len(warehouseIDs) != 2 || warehouseIDs[0] != "warehouse-one" || warehouseIDs[1] != "warehouse-two" {
		t.Fatalf("unexpected warehouse IDs in requests: %v", warehouseIDs)
	}
}

func TestExecSQLRejectsMissingWarehouse(t *testing.T) {
	err := (&clientData{}).execSQL(context.Background(), "", "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "warehouse_id") {
		t.Fatalf("expected missing warehouse error, got %v", err)
	}
}

func TestExecSQLReturnsStatementFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"statement_id":"statement-id","status":{"state":"FAILED","error":{"message":"simulated failure"}}}`))
	}))
	defer server.Close()

	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("create Databricks client: %v", err)
	}
	err = (&clientData{w: client}).execSQL(context.Background(), "warehouse", "SELECT invalid")
	if err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatalf("expected statement failure, got %v", err)
	}
}

func TestMetricViewImportNameParts(t *testing.T) {
	parts, ok := splitFullName("catalog.schema.view")
	if !ok || len(parts) != 3 || parts[0] != "catalog" || parts[1] != "schema" || parts[2] != "view" {
		t.Fatalf("unexpected import ID parts: %v, %t", parts, ok)
	}
	for _, invalid := range []string{"catalog.schema", "catalog..view", "catalog.schema.view.extra", "catalog.sch`ema.view"} {
		if _, ok := splitFullName(invalid); ok {
			t.Errorf("expected import ID %q to be rejected", invalid)
		}
	}
}

func TestMetricViewFullName(t *testing.T) {
	model := metricViewModel{
		CatalogName: types.StringValue("catalog"),
		SchemaName:  types.StringValue("schema"),
		Name:        types.StringValue("view"),
	}
	if got := metricViewFullName(model); got != "catalog.schema.view" {
		t.Fatalf("unexpected full name %q", got)
	}
}
