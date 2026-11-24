package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/databricks/databricks-sdk-go"
	"github.com/databricks/databricks-sdk-go/apierr"
	"github.com/databricks/databricks-sdk-go/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The Knowledge Asset Links API (/api/2.0/knowledge/pages/*/asset-link-categories/*/asset-links)
// is not part of the Databricks Go SDK yet, so requests go through the SDK's authenticated REST client.
// Asset links are immutable: there is no update endpoint, so every argument forces replacement.

var (
	_ resource.Resource                = &pageAssetLinkResource{}
	_ resource.ResourceWithConfigure   = &pageAssetLinkResource{}
	_ resource.ResourceWithImportState = &pageAssetLinkResource{}
)

const (
	linkCategorySource        = "SOURCE"
	linkCategoryRelatedAssets = "RELATED_ASSETS"

	assetTypeUCTable           = "ASSET_TYPE_UC_TABLE"
	assetTypeDashboard         = "ASSET_TYPE_WORKSPACE_DASHBOARD"
	assetTypeGenieAgent        = "ASSET_TYPE_WORKSPACE_GENIE_AGENT"
	assetTypeNotebook          = "ASSET_TYPE_WORKSPACE_NOTEBOOK"
	assetTypeApp               = "ASSET_TYPE_WORKSPACE_APP"
	assetTypeDBSQLQuery        = "ASSET_TYPE_WORKSPACE_DBSQL_QUERY"
	assetTypeAccountPage       = "ASSET_TYPE_ACCOUNT_PAGE"
	assetTypeAccountUser       = "ASSET_TYPE_ACCOUNT_USER"
	assetTypeExternalURL       = "ASSET_TYPE_EXTERNAL_URL"
	assetLinkCategoriesSegment = "asset-link-categories"
)

var (
	assetLinkCategories = []string{linkCategorySource, linkCategoryRelatedAssets}
	assetLinkTypes      = []string{
		assetTypeUCTable, assetTypeDashboard, assetTypeGenieAgent, assetTypeNotebook, assetTypeApp,
		assetTypeDBSQLQuery, assetTypeAccountPage, assetTypeAccountUser, assetTypeExternalURL,
	}
	assetLinkNameRe = regexp.MustCompile(`^(pages/[^/\s]+)/asset-link-categories/(SOURCE|RELATED_ASSETS)/asset-links/[^/\s]+$`)
)

func NewPageAssetLinkResource() resource.Resource { return &pageAssetLinkResource{} }

type pageAssetLinkResource struct {
	api *client.DatabricksClient
	w   *databricks.WorkspaceClient
}

type pageAssetLinkModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Page            types.String `tfsdk:"page"`
	Category        types.String `tfsdk:"category"`
	AssetType       types.String `tfsdk:"asset_type"`
	AssetScopeID    types.String `tfsdk:"asset_scope_id"`
	AssetID         types.String `tfsdk:"asset_id"`
	ResolvedAssetID types.String `tfsdk:"resolved_asset_id"`
	CreateTime      types.String `tfsdk:"create_time"`
}

// assetLinkAPI is the AssetLink object of the Knowledge API.
type assetLinkAPI struct {
	Name         string `json:"name,omitempty"`
	AssetType    string `json:"asset_type,omitempty"`
	AssetScopeID string `json:"asset_scope_id,omitempty"`
	AssetID      string `json:"asset_id,omitempty"`
	CreateTime   string `json:"create_time,omitempty"`
}

func (r *pageAssetLinkResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "databricks_page_asset_link"
}

func (r *pageAssetLinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Link from a Databricks Knowledge page to an asset (Sources or Related assets list). " +
			"Links are immutable, so changing any argument replaces the link.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Same as name.",
				PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned resource name: pages/{page_id}/asset-link-categories/{category}/asset-links/{link_id}.",
				PlanModifiers: keep,
			},
			"page": schema.StringAttribute{
				Required:      true,
				Description:   "Page resource name in the form pages/{page_id} (for example databricks_page.x.name).",
				Validators:    []validator.String{regexStringValidator{re: pageNameRe, what: "pages/{page_id}"}},
				PlanModifiers: replace,
			},
			"category": schema.StringAttribute{
				Required:      true,
				Description:   "SOURCE (Sources list) or RELATED_ASSETS (Related assets list).",
				Validators:    []validator.String{oneOfStringValidator{values: assetLinkCategories}},
				PlanModifiers: replace,
			},
			"asset_type": schema.StringAttribute{
				Required:      true,
				Description:   "Target type: " + strings.Join(assetLinkTypes, ", ") + ".",
				Validators:    []validator.String{oneOfStringValidator{values: assetLinkTypes}},
				PlanModifiers: replace,
			},
			"asset_scope_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Scope that contains the asset, as a bare ID: metastore ID for UC tables, workspace ID for " +
					"workspace assets, account ID for account pages, users and external URLs. If omitted, it is " +
					"looked up from the current workspace (the account ID is taken from DATABRICKS_ACCOUNT_ID or account_id in the ~/.databrickscfg profile).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"asset_id": schema.StringAttribute{
				Required: true,
				Description: "Identifier of the target within the scope: table full name, dashboard/query/Genie space/app ID, " +
					"notebook ID, page ID or user ID (bare, without a resource-name prefix), or the full URL for external links.",
				Validators:    []validator.String{lengthStringValidator{min: 1, max: 4096}},
				PlanModifiers: replace,
			},
			"resolved_asset_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Canonical asset ID returned by the server (for example the table FQN or native UUID).",
				PlanModifiers: keep,
			},
			"create_time": schema.StringAttribute{
				Computed:      true,
				Description:   "Creation time (RFC 3339).",
				PlanModifiers: keep,
			},
		},
	}
}

func (r *pageAssetLinkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*clientData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *clientData, got %T", req.ProviderData))
		return
	}
	api, err := client.New(c.w.Config)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Databricks REST client", err.Error())
		return
	}
	r.api = api
	r.w = c.w
}

// ---------- CRUD ----------

func (r *pageAssetLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pageAssetLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	assetType := plan.AssetType.ValueString()
	scope := plan.AssetScopeID.ValueString()
	if plan.AssetScopeID.IsNull() || plan.AssetScopeID.IsUnknown() || scope == "" {
		var err error
		if scope, err = r.defaultAssetScopeID(ctx, assetType); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("asset_scope_id"), "Unable to determine asset_scope_id", err.Error())
			return
		}
	}

	parent := plan.Page.ValueString() + "/" + assetLinkCategoriesSegment + "/" + plan.Category.ValueString()
	body := assetLinkAPI{AssetType: assetType, AssetScopeID: scope, AssetID: plan.AssetID.ValueString()}
	var created assetLinkAPI
	if err := r.api.Do(ctx, http.MethodPost, pageAPIPath(parent)+"/asset-links", nil, nil, body, &created); err != nil {
		resp.Diagnostics.AddError("Failed to create asset link on "+parent, err.Error())
		return
	}
	if created.Name == "" {
		resp.Diagnostics.AddError("Failed to create asset link on "+parent, "API response did not contain a link name")
		return
	}

	plan.Name = types.StringValue(created.Name)
	plan.ID = plan.Name
	plan.AssetScopeID = types.StringValue(scope)
	plan.ResolvedAssetID = types.StringValue(firstNonEmpty(created.AssetID, plan.AssetID.ValueString()))
	plan.CreateTime = types.StringValue(created.CreateTime)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *pageAssetLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state pageAssetLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := firstNonEmpty(state.ID.ValueString(), state.Name.ValueString())

	var link assetLinkAPI
	if err := r.api.Do(ctx, http.MethodGet, pageAPIPath(name), nil, nil, nil, &link); err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			// link (or its target) is gone -> plan will recreate it
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read asset link "+name, err.Error())
		return
	}
	if link.Name == "" {
		link.Name = name
	}
	page, category, ok := parseAssetLinkName(link.Name)
	if !ok {
		resp.Diagnostics.AddError("Unexpected asset link name", "API returned: "+link.Name)
		return
	}

	state.Name = types.StringValue(link.Name)
	state.ID = state.Name
	state.Page = types.StringValue(page)
	state.Category = types.StringValue(category)
	state.AssetType = types.StringValue(link.AssetType)
	state.CreateTime = types.StringValue(link.CreateTime)
	if link.AssetScopeID != "" {
		state.AssetScopeID = types.StringValue(link.AssetScopeID)
	}
	if link.AssetID != "" {
		state.ResolvedAssetID = types.StringValue(link.AssetID)
	}
	// The server normalizes asset_id (table FQN, native UUIDs). Keep the configured value so that
	// equivalent inputs do not cause replacement; on import there is no prior value, so use the API one.
	if state.AssetID.IsNull() || state.AssetID.IsUnknown() || state.AssetID.ValueString() == "" {
		state.AssetID = state.ResolvedAssetID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is only reached when nothing but computed values changed: every argument forces replacement.
func (r *pageAssetLinkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state pageAssetLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.Name = state.ID, state.Name
	plan.ResolvedAssetID, plan.CreateTime = state.ResolvedAssetID, state.CreateTime
	if plan.AssetScopeID.IsUnknown() || plan.AssetScopeID.IsNull() {
		plan.AssetScopeID = state.AssetScopeID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *pageAssetLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pageAssetLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	if err := r.api.Do(ctx, http.MethodDelete, pageAPIPath(name), nil, nil, nil, nil); err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete asset link "+name, err.Error())
	}
}

// ImportState expects the full link name: pages/{page_id}/asset-link-categories/{category}/asset-links/{link_id}.
func (r *pageAssetLinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name := strings.TrimSpace(req.ID)
	page, category, ok := parseAssetLinkName(name)
	if !ok {
		resp.Diagnostics.AddError("Invalid asset link import ID",
			"Expected pages/{page_id}/asset-link-categories/{SOURCE|RELATED_ASSETS}/asset-links/{link_id}, got: "+req.ID)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("page"), page)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("category"), category)...)
}

// ---------- helpers ----------

func parseAssetLinkName(name string) (page, category string, ok bool) {
	m := assetLinkNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// defaultAssetScopeID resolves the scope expected by the API for the given asset type.
func (r *pageAssetLinkResource) defaultAssetScopeID(ctx context.Context, assetType string) (string, error) {
	if r.w == nil {
		return "", fmt.Errorf("set asset_scope_id explicitly")
	}
	switch assetType {
	case assetTypeUCTable:
		ms, err := r.w.Metastores.Current(ctx)
		if err != nil {
			return "", fmt.Errorf("looking up the current metastore: %w; set asset_scope_id to the metastore ID", err)
		}
		return ms.MetastoreId, nil
	case assetTypeDashboard, assetTypeGenieAgent, assetTypeNotebook, assetTypeApp, assetTypeDBSQLQuery:
		id, err := r.w.CurrentWorkspaceID(ctx)
		if err != nil {
			return "", fmt.Errorf("looking up the workspace ID: %w; set asset_scope_id to the workspace ID", err)
		}
		return strconv.FormatInt(id, 10), nil
	default: // account-level assets: pages, users, external URLs
		if id := r.w.Config.AccountID; id != "" {
			return id, nil
		}
		return "", fmt.Errorf("%s links need the account ID: set asset_scope_id, DATABRICKS_ACCOUNT_ID, or account_id in the profile", assetType)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
