package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/databricks/databricks-sdk-go"
	"github.com/databricks/databricks-sdk-go/service/sql"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &metricViewProvider{}

// metricViewProvider exposes the provider's custom Databricks resources.
// Provider type name is "databricks" so resources use the databricks_ prefix.
type metricViewProvider struct {
	version string
}

type providerModel struct {
	Host    types.String `tfsdk:"host"`
	Token   types.String `tfsdk:"token"`
	Profile types.String `tfsdk:"profile"`
}

// clientData is handed to resources via ProviderData.
type clientData struct {
	w *databricks.WorkspaceClient
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &metricViewProvider{version: version}
	}
}

func (p *metricViewProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "databricks"
	resp.Version = p.version
}

func (p *metricViewProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages custom Unity Catalog resources. Authentication uses Databricks unified auth " +
			"(DATABRICKS_HOST / DATABRICKS_TOKEN env vars, ~/.databrickscfg profiles, Azure CLI, OAuth, ...).",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "Workspace URL. Falls back to DATABRICKS_HOST / profile.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Personal access token. Falls back to DATABRICKS_TOKEN / other unified auth methods.",
			},
			"profile": schema.StringAttribute{
				Optional:    true,
				Description: "Profile name from ~/.databrickscfg.",
			},
		},
	}
}

func (p *metricViewProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dbCfg := &databricks.Config{
		Host:    cfg.Host.ValueString(),
		Token:   cfg.Token.ValueString(),
		Profile: cfg.Profile.ValueString(),
	}

	w, err := databricks.NewWorkspaceClient(dbCfg)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Databricks client", err.Error())
		return
	}

	c := &clientData{w: w}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *metricViewProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewMetricViewResource, NewMaterializedViewResource, NewPageResource}
}

func (p *metricViewProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// execSQL runs a statement on the requested SQL warehouse and waits for completion.
func (c *clientData) execSQL(ctx context.Context, warehouseID, statement string) error {
	_, err := c.execSQLWithResult(ctx, warehouseID, statement)
	return err
}

// execSQLWithResult runs a statement on the requested warehouse and returns its result rows.
func (c *clientData) execSQLWithResult(ctx context.Context, warehouseID, statement string) (*sql.StatementResponse, error) {
	if strings.TrimSpace(warehouseID) == "" {
		return nil, fmt.Errorf("warehouse_id must not be empty")
	}
	resp, err := c.w.StatementExecution.ExecuteStatement(ctx, sql.ExecuteStatementRequest{
		WarehouseId:   warehouseID,
		Statement:     statement,
		WaitTimeout:   "30s",
		OnWaitTimeout: sql.ExecuteStatementRequestOnWaitTimeoutContinue,
	})
	if err != nil {
		return nil, err
	}

	for {
		if resp.Status == nil {
			return nil, fmt.Errorf("statement %s returned no status", resp.StatementId)
		}
		switch resp.Status.State {
		case sql.StatementStateSucceeded:
			return resp, nil
		case sql.StatementStateFailed, sql.StatementStateCanceled, sql.StatementStateClosed:
			msg := string(resp.Status.State)
			if resp.Status.Error != nil && resp.Status.Error.Message != "" {
				msg = resp.Status.Error.Message
			}
			return nil, fmt.Errorf("statement %s failed: %s", resp.StatementId, msg)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, err = c.w.StatementExecution.GetStatement(ctx, sql.GetStatementRequest{StatementId: resp.StatementId})
		if err != nil {
			return nil, err
		}
	}
}
