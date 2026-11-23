package provider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/databricks/databricks-sdk-go/apierr"
	"github.com/databricks/databricks-sdk-go/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The Knowledge Pages API (/api/2.0/knowledge/pages) is not part of the Databricks Go SDK yet,
// so requests are sent through the SDK's authenticated REST client.

var (
	_ resource.Resource                = &pageResource{}
	_ resource.ResourceWithConfigure   = &pageResource{}
	_ resource.ResourceWithImportState = &pageResource{}
)

const (
	pageStateDraft     = "DRAFT"
	pageStatePublished = "PUBLISHED"
	pagesCollection    = "/api/2.0/knowledge/pages"
)

var (
	pageNameRe   = regexp.MustCompile(`^pages/[^/\s]+$`)
	domainNameRe = regexp.MustCompile(`^domains/[^/\s]+$`)
)

func NewPageResource() resource.Resource { return &pageResource{} }

type pageResource struct {
	api *client.DatabricksClient
}

type pageModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Domain      types.String `tfsdk:"domain"`
	DisplayName types.String `tfsdk:"display_name"`
	Body        types.String `tfsdk:"body"`
	Synonyms    types.Set    `tfsdk:"synonyms"`
	Description types.String `tfsdk:"description"`
	State       types.String `tfsdk:"state"`
}

// pageAPI is the Page object of the Knowledge API.
type pageAPI struct {
	Name        string   `json:"name,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	Body        string   `json:"body,omitempty"`
	Synonyms    []string `json:"synonyms,omitempty"`
	Description string   `json:"description,omitempty"`
	State       string   `json:"state,omitempty"`
}

func (r *pageResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "databricks_page"
}

func (r *pageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Databricks Knowledge page (markdown document within a domain).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Same as name (pages/{page_id}).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned resource name in the form pages/{page_id}.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"domain": schema.StringAttribute{
				Required:    true,
				Description: "Full domain resource name in the form domains/{domain_id}.",
				Validators:  []validator.String{regexStringValidator{re: domainNameRe, what: "domains/{domain_id}"}},
			},
			"display_name": schema.StringAttribute{
				Required:    true,
				Description: "Page title, 1-200 characters. Published titles must be unique within the domain.",
				Validators:  []validator.String{lengthStringValidator{min: 1, max: 200}},
			},
			"body": schema.StringAttribute{
				Optional:    true,
				Description: "Markdown content of the page.",
			},
			"synonyms": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Alternative names (up to 50, each 1-200 characters, no surrounding whitespace). " +
					"The server deduplicates and sorts them, so they are modelled as a set.",
				Validators: []validator.Set{synonymsValidator{}},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Short description of the page.",
			},
			"state": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(pageStateDraft),
				Description: "DRAFT (default) or PUBLISHED.",
				Validators:  []validator.String{oneOfStringValidator{values: []string{pageStateDraft, pageStatePublished}}},
			},
		},
	}
}

func (r *pageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
}

// ---------- CRUD ----------

func (r *pageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	synonyms, diags := setToStrings(ctx, plan.Synonyms)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := pageAPI{
		Domain:      plan.Domain.ValueString(),
		DisplayName: plan.DisplayName.ValueString(),
		Body:        plan.Body.ValueString(),
		Synonyms:    synonyms,
		Description: plan.Description.ValueString(),
		State:       plan.State.ValueString(),
	}
	query := map[string]any{}
	if id, err := newUUIDv4(); err == nil {
		query["request_id"] = id // makes SDK-level retries idempotent
	}
	var created pageAPI
	if err := r.api.Do(ctx, http.MethodPost, pagesCollection, nil, query, body, &created); err != nil {
		resp.Diagnostics.AddError("Failed to create page "+plan.DisplayName.ValueString(), err.Error())
		return
	}
	if created.Name == "" {
		resp.Diagnostics.AddError("Failed to create page "+plan.DisplayName.ValueString(), "API response did not contain a page name")
		return
	}

	plan.Name = types.StringValue(created.Name)
	plan.ID = plan.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *pageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state pageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	if name == "" {
		name = state.Name.ValueString()
	}

	var page pageAPI
	if err := r.api.Do(ctx, http.MethodGet, pageAPIPath(name), nil, nil, nil, &page); err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			// deleted outside Terraform -> plan will recreate it
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read page "+name, err.Error())
		return
	}
	if page.Name == "" {
		page.Name = name
	}

	resp.Diagnostics.Append(applyPageAPI(&state, page)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state pageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()

	body, mask, diags := pageUpdatePayload(ctx, state, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(mask) > 0 {
		query := map[string]any{"update_mask": strings.Join(mask, ",")}
		var updated pageAPI
		if err := r.api.Do(ctx, http.MethodPatch, pageAPIPath(name), nil, query, body, &updated); err != nil {
			resp.Diagnostics.AddError("Failed to update page "+name, err.Error())
			return
		}
	}

	plan.Name = types.StringValue(name)
	plan.ID = plan.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *pageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	if err := r.api.Do(ctx, http.MethodDelete, pageAPIPath(name), nil, nil, nil, nil); err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete page "+name, err.Error())
	}
}

// ImportState accepts either "pages/{page_id}" or a bare "{page_id}".
func (r *pageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name, ok := normalizePageName(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid page import ID", "Expected pages/{page_id} or {page_id}, got: "+req.ID)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// ---------- helpers ----------

func normalizePageName(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "pages/") {
		id = "pages/" + id
	}
	return id, pageNameRe.MatchString(id)
}

// applyPageAPI copies API values into the model. Optional attributes that are empty in the
// API keep a null value when they were null before, so unset arguments do not produce diffs.
func applyPageAPI(m *pageModel, p pageAPI) diag.Diagnostics {
	m.Name = types.StringValue(p.Name)
	m.ID = m.Name
	m.Domain = types.StringValue(p.Domain)
	m.DisplayName = types.StringValue(p.DisplayName)
	m.Body = pageOptionalString(p.Body, m.Body)
	m.Description = pageOptionalString(p.Description, m.Description)
	if p.State == "" {
		p.State = pageStateDraft
	}
	m.State = types.StringValue(p.State)

	if len(p.Synonyms) == 0 && (m.Synonyms.IsNull() || m.Synonyms.IsUnknown()) {
		m.Synonyms = types.SetNull(types.StringType)
		return nil
	}
	elems := make([]attr.Value, 0, len(p.Synonyms))
	for _, s := range p.Synonyms {
		elems = append(elems, types.StringValue(s))
	}
	set, diags := types.SetValue(types.StringType, elems)
	m.Synonyms = set
	return diags
}

func pageOptionalString(api string, prior types.String) types.String {
	if api == "" && (prior.IsNull() || prior.IsUnknown()) {
		return types.StringNull()
	}
	return types.StringValue(api)
}

// pageUpdatePayload returns the PATCH body and update_mask for the fields that changed.
// A map is used so that cleared fields are sent explicitly ("" / []), not omitted.
func pageUpdatePayload(ctx context.Context, state, plan pageModel) (map[string]any, []string, diag.Diagnostics) {
	body := map[string]any{}
	var mask []string
	add := func(field string, value any) {
		body[field] = value
		mask = append(mask, field)
	}
	if !plan.Domain.Equal(state.Domain) {
		add("domain", plan.Domain.ValueString())
	}
	if !plan.DisplayName.Equal(state.DisplayName) {
		add("display_name", plan.DisplayName.ValueString())
	}
	if plan.Body.ValueString() != state.Body.ValueString() {
		add("body", plan.Body.ValueString())
	}
	if plan.Description.ValueString() != state.Description.ValueString() {
		add("description", plan.Description.ValueString())
	}
	if !plan.State.Equal(state.State) {
		add("state", plan.State.ValueString())
	}
	oldSyn, diags := setToStrings(ctx, state.Synonyms)
	newSyn, d := setToStrings(ctx, plan.Synonyms)
	diags.Append(d...)
	if !sameStringSet(oldSyn, newSyn) {
		add("synonyms", newSyn) // an empty list clears synonyms
	}
	return body, mask, diags
}

func setToStrings(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	out := []string{}
	if s.IsNull() || s.IsUnknown() {
		return out, nil
	}
	diags := s.ElementsAs(ctx, &out, false)
	return out, diags
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, v := range a {
		seen[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := seen[v]; !ok {
			return false
		}
	}
	return true
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ---------- validators ----------

type regexStringValidator struct {
	re   *regexp.Regexp
	what string
}

func (v regexStringValidator) Description(_ context.Context) string {
	return "must have the form " + v.what
}
func (v regexStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v regexStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !v.re.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value",
			fmt.Sprintf("Expected %s, got: %s", v.what, req.ConfigValue.ValueString()))
	}
}

type lengthStringValidator struct{ min, max int }

func (v lengthStringValidator) Description(_ context.Context) string {
	return fmt.Sprintf("must be %d-%d characters long", v.min, v.max)
}
func (v lengthStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v lengthStringValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := len([]rune(req.ConfigValue.ValueString())); n < v.min || n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid length", v.Description(ctx))
	}
}

type oneOfStringValidator struct{ values []string }

func (v oneOfStringValidator) Description(_ context.Context) string {
	return "must be one of: " + strings.Join(v.values, ", ")
}
func (v oneOfStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v oneOfStringValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, allowed := range v.values {
		if req.ConfigValue.ValueString() == allowed {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", v.Description(ctx)+", got: "+req.ConfigValue.ValueString())
}

type synonymsValidator struct{}

func (synonymsValidator) Description(_ context.Context) string {
	return "at most 50 synonyms, each 1-200 characters without leading/trailing whitespace"
}
func (v synonymsValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (synonymsValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > 50 {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many synonyms", fmt.Sprintf("at most 50 allowed, got %d", len(elems)))
	}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		v := s.ValueString()
		if n := len([]rune(v)); n < 1 || n > 200 {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid synonym", fmt.Sprintf("synonym must be 1-200 characters, got %q", v))
		} else if strings.TrimSpace(v) != v {
			// the server trims values, which would cause a perpetual diff
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid synonym", fmt.Sprintf("synonym must not have leading/trailing whitespace: %q", v))
		}
	}
}

// pageAPIPath maps a page resource name (pages/{id}) to its REST path.
func pageAPIPath(name string) string { return "/api/2.0/knowledge/" + name }
