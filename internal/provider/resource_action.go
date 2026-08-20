package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ActionResource{}
var _ resource.ResourceWithImportState = &ActionResource{}

type ActionResource struct {
	client *client
}

func NewActionResource() resource.Resource {
	return &ActionResource{}
}

type actionResourceModel struct {
	ID                       types.Int64  `tfsdk:"id"`
	Name                     types.String `tfsdk:"name"`
	Type                     types.String `tfsdk:"type"`
	Enabled                  types.Bool   `tfsdk:"enabled"`
	FilterID                 types.Int64  `tfsdk:"filter_id"`
	ClientID                 types.Int64  `tfsdk:"client_id"`
	ExternalDownloadClientID types.Int64  `tfsdk:"external_download_client_id"`
	Category                 types.String `tfsdk:"category"`
	Label                    types.String `tfsdk:"label"`
	DownloadPath             types.String `tfsdk:"download_path"`
	Paused                   types.Bool   `tfsdk:"paused"`
	WebhookHost              types.String `tfsdk:"webhook_host"`
	WebhookType              types.String `tfsdk:"webhook_type"`
	WebhookMethod            types.String `tfsdk:"webhook_method"`
	WebhookData              types.String `tfsdk:"webhook_data"`
}

func (r *ActionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_action"
}

func (r *ActionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An autobrr filter action (what to do with a matched release). " +
			"IMPORTANT: autobrr's own PUT /api/actions/{id} has a live-confirmed bug - it inserts " +
			"a new row instead of updating in place, rather than updating it. Every attribute below " +
			"is RequiresReplace as a result: any change to a real value replaces the action (delete " +
			"old id, create new one) via Terraform's own replace machinery, rather than this " +
			"provider hand-rolling an in-place update that changes id out from under Terraform " +
			"(that shape produced a real 'provider produced inconsistent result' error, confirmed " +
			"live - RequiresReplace is the correct fix, not a workaround for it).",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed: true,
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "SONARR, RADARR, DELUGE_V2, TEST, etc. - see autobrr's ActionType.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enabled": schema.BoolAttribute{
				Required: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"filter_id": schema.Int64Attribute{
				Required:    true,
				Description: "The autobrr_filter this action belongs to.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"client_id": schema.Int64Attribute{
				Optional:    true,
				Description: "The autobrr_download_client to act through.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"external_download_client_id": schema.Int64Attribute{
				Optional:    true,
				Description: "For SONARR/RADARR-type actions: overrides which download client the *arr app itself uses for this push, scoping it separately from that app's default/RSS-driven client.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"category": schema.StringAttribute{
				Optional:    true,
				Description: "qBittorrent-style category assignment. NOT what sets a Transmission label - see \"label\" below (confirmed against autobrr's own internal/action/transmission.go: only action.Label is sent to TorrentSet's Labels field; Category is unused by the Transmission action type).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"label": schema.StringAttribute{
				Optional:    true,
				Description: "For TRANSMISSION-type actions: sets the torrent's label(s) at add time (comma-separated in autobrr's own model, though this provider treats it as one string). This is autobrr's own generic assignment mechanism - independent of any *arr app's category field (e.g. Sonarr's tvCategory) - so it works the same way for filters with no *arr in the loop at all (e.g. music).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"download_path": schema.StringAttribute{
				Optional:    true,
				Description: "For direct-download actions (e.g. DELUGE_V2): destination path on that client.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"paused": schema.BoolAttribute{
				Optional:    true,
				Description: "Add the torrent in a paused state.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"webhook_host": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "For WEBHOOK-type actions: the destination URL, always POSTed to. Sensitive since a caller with no other auth surface (autobrr's own WebhookHeaders is a dead field - see webhook_data) typically has to embed a shared-secret token as a query param here.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"webhook_type": schema.StringAttribute{
				Optional:    true,
				Description: "For WEBHOOK-type actions: content-type hint (e.g. \"json\"). autobrr's own webhook() always sends Content-Type: application/json regardless (confirmed via source), so this is informational in autobrr's UI rather than functionally load-bearing, but the API still expects it.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"webhook_method": schema.StringAttribute{
				Optional:    true,
				Description: "For WEBHOOK-type actions: HTTP method. autobrr's own webhook() hardcodes POST regardless of this value (confirmed via source) - set for API-shape completeness, not functional effect.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"webhook_data": schema.StringAttribute{
				Optional:    true,
				Description: "For WEBHOOK-type actions: the POST body, a plain string autobrr passes through its own Go-template engine against the matched release (e.g. {{.DownloadURL}}) before sending - see autobrr's own release template docs for available fields.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *ActionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("expected *client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *ActionResource) modelToAction(m actionResourceModel) *action {
	return &action{
		ID:                       int(m.ID.ValueInt64()),
		Name:                     m.Name.ValueString(),
		Type:                     m.Type.ValueString(),
		Enabled:                  m.Enabled.ValueBool(),
		FilterID:                 int(m.FilterID.ValueInt64()),
		ClientID:                 int32(m.ClientID.ValueInt64()),
		ExternalDownloadClientID: int32(m.ExternalDownloadClientID.ValueInt64()),
		Category:                 m.Category.ValueString(),
		Label:                    m.Label.ValueString(),
		DownloadPath:             m.DownloadPath.ValueString(),
		Paused:                   m.Paused.ValueBool(),
		WebhookHost:              m.WebhookHost.ValueString(),
		WebhookType:              m.WebhookType.ValueString(),
		WebhookMethod:            m.WebhookMethod.ValueString(),
		WebhookData:              m.WebhookData.ValueString(),
	}
}

// actionToModel builds state from the API response. filter_id is NEVER
// trusted from it - GET /api/actions (the only read endpoint; there's no
// GET-by-id) omits filter_id from every entry entirely, confirmed live -
// so it's preserved from prior state instead, same rationale as the
// sensitive fields elsewhere in this provider (can't be read back, must
// be carried forward).
func (r *ActionResource) actionToModel(a *action, prior actionResourceModel) actionResourceModel {
	m := actionResourceModel{
		ID:       types.Int64Value(int64(a.ID)),
		Name:     types.StringValue(a.Name),
		Type:     types.StringValue(a.Type),
		Enabled:  types.BoolValue(a.Enabled),
		FilterID: prior.FilterID,
	}
	if a.Paused {
		m.Paused = types.BoolValue(true)
	}
	if a.ClientID != 0 {
		m.ClientID = types.Int64Value(int64(a.ClientID))
	}
	if a.ExternalDownloadClientID != 0 {
		m.ExternalDownloadClientID = types.Int64Value(int64(a.ExternalDownloadClientID))
	}
	if a.Category != "" {
		m.Category = types.StringValue(a.Category)
	}
	if a.Label != "" {
		m.Label = types.StringValue(a.Label)
	}
	if a.DownloadPath != "" {
		m.DownloadPath = types.StringValue(a.DownloadPath)
	}
	// Unlike indexer/feed secret fields elsewhere in this provider,
	// confirmed live that GET /api/filters/{id} returns webhook_host
	// unmasked (a real URL, not a "<redacted>" placeholder) - autobrr has
	// no equivalent masking for webhook fields, so these can be read back
	// directly rather than needing the preserve-from-prior-state
	// workaround FilterID above uses.
	if a.WebhookHost != "" {
		m.WebhookHost = types.StringValue(a.WebhookHost)
	}
	if a.WebhookType != "" {
		m.WebhookType = types.StringValue(a.WebhookType)
	}
	if a.WebhookMethod != "" {
		m.WebhookMethod = types.StringValue(a.WebhookMethod)
	}
	if a.WebhookData != "" {
		m.WebhookData = types.StringValue(a.WebhookData)
	}
	return m
}

func (r *ActionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan actionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.createAction(r.modelToAction(plan))
	if err != nil {
		resp.Diagnostics.AddError("Error creating action", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.actionToModel(created, plan))...)
}

func (r *ActionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state actionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	a, err := r.client.getAction(int(state.ID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Error reading action", err.Error())
		return
	}
	if a == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.actionToModel(a, state))...)
}

// Update works around the live PUT /api/actions/{id} bug (see this
// resource's Schema description) by deleting the old action and creating
// a fresh one - the id changes as a result. This is a deliberate,
// documented tradeoff, not an oversight.
func (r *ActionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state actionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	var plan actionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteAction(int(state.ID.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Error deleting old action during update", err.Error())
		return
	}

	created, err := r.client.createAction(r.modelToAction(plan))
	if err != nil {
		resp.Diagnostics.AddError("Error creating replacement action during update", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.actionToModel(created, plan))...)
}

func (r *ActionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state actionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteAction(int(state.ID.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Error deleting action", err.Error())
		return
	}
}

func (r *ActionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric action id, got %q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
