package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &IndexerResource{}
var _ resource.ResourceWithImportState = &IndexerResource{}

type IndexerResource struct {
	client *client
}

func NewIndexerResource() resource.Resource {
	return &IndexerResource{}
}

type indexerResourceModel struct {
	ID                 types.Int64  `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Identifier         types.String `tfsdk:"identifier"`
	IdentifierExternal types.String `tfsdk:"identifier_external"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	Implementation     types.String `tfsdk:"implementation"`
	BaseURL            types.String `tfsdk:"base_url"`
	Settings           types.Map    `tfsdk:"settings"`
}

func (r *IndexerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_indexer"
}

func (r *IndexerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An autobrr indexer instance (a tracker registered against a base implementation - " +
			"irc/rss/torznab/newznab). For a tracker with no built-in autobrr definition (e.g. an RSS-only " +
			"tracker), this is the \"Generic RSS\"-style instance the UI would create, paired with one or " +
			"more autobrr_feed resources pointing indexer_id back at it - autobrr has no single combined " +
			"resource for this, an indexer and its feed(s) are always separate API objects.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name, e.g. \"DeepBassNine All\".",
			},
			"identifier": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "For rss/torznab/newznab: leave unset, autobrr generates it from slug(implementation-name) on create and this provider never sends a client value for those (confirmed in autobrr's own internal/indexer/service.go Store() - only feed-type implementations get regenerated there). " +
					"For irc: REQUIRED - must be the exact identifier of a real built-in autobrr indexer definition (e.g. \"ops\" for Orpheus, \"btn\" for BroadcasTheNet - see autobrr's internal/indexer/definitions/*.yaml). autobrr's Store() does NOT auto-generate identifiers for irc-type indexers - mapIndexer() looks up the built-in definition template by this exact string, so an empty or wrong value silently produces an indexer with no parse rules/settings schema attached, not an error. " +
					"Never changes after create either way (Update() persists whatever is already there verbatim, it does not regenerate).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"identifier_external": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Defaults to name if left empty (autobrr's own Store() behavior, create-time only).",
			},
			"enabled": schema.BoolAttribute{
				Required: true,
			},
			"implementation": schema.StringAttribute{
				Required:    true,
				Description: "irc, rss, torznab, or newznab. Changing this on an existing indexer is unsupported/undocumented on autobrr's side - treated as RequiresReplace here rather than risking an in-place change autobrr doesn't actually handle.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"base_url": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Only meaningful for irc-type indexers (autobrr requires it non-empty there). Leave unset for rss/torznab/newznab.",
			},
			"settings": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				Description: "Implementation-specific key/value settings (e.g. an IRC-type private tracker's authkey/torrent_pass/api_key). Empty/unset for a plain rss-implementation indexer - the real per-feed credentials live on autobrr_feed.url/api_key/cookie instead, not here.",
			},
		},
	}
}

func (r *IndexerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *IndexerResource) modelToIndexer(ctx context.Context, m indexerResourceModel) (*indexer, error) {
	var settings map[string]string
	if !m.Settings.IsUnknown() && !m.Settings.IsNull() {
		if err := m.Settings.ElementsAs(ctx, &settings, false); err != nil {
			return nil, fmt.Errorf("reading settings: %v", err)
		}
	}
	return &indexer{
		ID:                 m.ID.ValueInt64(),
		Name:               m.Name.ValueString(),
		Identifier:         m.Identifier.ValueString(),
		IdentifierExternal: m.IdentifierExternal.ValueString(),
		Enabled:            m.Enabled.ValueBool(),
		Implementation:     m.Implementation.ValueString(),
		BaseURL:            m.BaseURL.ValueString(),
		Settings:           settings,
	}, nil
}

func (r *IndexerResource) indexerToModel(ctx context.Context, i *indexer) (indexerResourceModel, error) {
	settingsMap, diags := types.MapValueFrom(ctx, types.StringType, i.Settings)
	if diags.HasError() {
		return indexerResourceModel{}, fmt.Errorf("converting settings: %v", diags)
	}
	return indexerResourceModel{
		ID:                 types.Int64Value(i.ID),
		Name:               types.StringValue(i.Name),
		Identifier:         types.StringValue(i.Identifier),
		IdentifierExternal: types.StringValue(i.IdentifierExternal),
		Enabled:            types.BoolValue(i.Enabled),
		Implementation:     types.StringValue(i.Implementation),
		BaseURL:            types.StringValue(i.BaseURL),
		Settings:           settingsMap,
	}, nil
}

func (r *IndexerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan indexerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	i, err := r.modelToIndexer(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error building indexer", err.Error())
		return
	}
	// Only feed-type implementations get a server-generated identifier
	// (isImplFeed in autobrr's own service.go) - clearing it there avoids
	// ever fighting the server's own slug(implementation-name) logic.
	// irc-type indexers have NO such auto-generation and MUST carry the
	// exact built-in definition identifier the config supplied (e.g.
	// "ops") - clearing it here would silently create an indexer with no
	// definition attached instead of erroring, so it must be left alone.
	switch i.Implementation {
	case "rss", "torznab", "newznab":
		i.Identifier = ""
	}

	created, err := r.client.createIndexer(i)
	if err != nil {
		resp.Diagnostics.AddError("Error creating indexer", err.Error())
		return
	}

	model, err := r.indexerToModel(ctx, created)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding indexer", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *IndexerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state indexerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	i, err := r.client.getIndexer(state.ID.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Error reading indexer", err.Error())
		return
	}
	if i == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	model, err := r.indexerToModel(ctx, i)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding indexer", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *IndexerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan indexerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	i, err := r.modelToIndexer(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error building indexer", err.Error())
		return
	}
	// Update() persists whatever identifier is sent verbatim (it does NOT
	// regenerate it) - plan.Identifier is already carried forward from
	// state via UseStateForUnknown, so this is just making that explicit.
	if err := r.client.updateIndexer(i); err != nil {
		resp.Diagnostics.AddError("Error updating indexer", err.Error())
		return
	}

	updated, err := r.client.getIndexer(i.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading back updated indexer", err.Error())
		return
	}
	if updated == nil {
		resp.Diagnostics.AddError("Error reading back updated indexer", "indexer disappeared immediately after update")
		return
	}

	model, err := r.indexerToModel(ctx, updated)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding indexer", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *IndexerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state indexerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteIndexer(state.ID.ValueInt64()); err != nil {
		resp.Diagnostics.AddError("Error deleting indexer", err.Error())
		return
	}
}

func (r *IndexerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric indexer id, got %q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
