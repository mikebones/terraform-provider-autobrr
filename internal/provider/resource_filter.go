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
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &FilterResource{}
var _ resource.ResourceWithImportState = &FilterResource{}

type FilterResource struct {
	client *client
}

func NewFilterResource() resource.Resource {
	return &FilterResource{}
}

// filterResourceModel is deliberately narrow - autobrr's real filter table
// has ~70 columns. This provider only manages the structural fields
// needed for the tv/movies use case (enabled, indexer scope, basic
// quality/announce matching). Everything else (fine-grained quality
// tuning, e.g. music's genre-tag filters) stays live-editable via
// autobrr's own UI without Terraform fighting it on every apply.
type filterResourceModel struct {
	ID             types.Int64  `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	IndexerIDs     types.List   `tfsdk:"indexer_ids"`
	Resolutions    types.List   `tfsdk:"resolutions"`
	AnnounceTypes  types.List   `tfsdk:"announce_types"`
	ExceptReleases types.String `tfsdk:"except_releases"`
}

func (r *FilterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_filter"
}

func (r *FilterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An autobrr filter - narrow schema by design (enabled/indexer-scope/basic matching only). " +
			"See package doc comment on filterResourceModel for why.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
			},
			"enabled": schema.BoolAttribute{
				Required: true,
			},
			"indexer_ids": schema.ListAttribute{
				Required:    true,
				ElementType: types.Int64Type,
				Description: "Indexer ids this filter applies to (autobrr's own indexer ids, not Prowlarr's).",
			},
			"resolutions": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},
			"announce_types": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
			},
			"except_releases": schema.StringAttribute{
				Optional: true,
				Computed: true,
			},
		},
	}
}

func (r *FilterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FilterResource) modelToFilter(ctx context.Context, m filterResourceModel) (*filter, error) {
	var indexerIDs []int64
	if err := m.IndexerIDs.ElementsAs(ctx, &indexerIDs, false); err != nil {
		return nil, fmt.Errorf("reading indexer_ids: %v", err)
	}
	indexers := make([]filterIndexerRef, len(indexerIDs))
	for i, id := range indexerIDs {
		indexers[i] = filterIndexerRef{ID: int(id)}
	}

	var resolutions []string
	if !m.Resolutions.IsUnknown() && !m.Resolutions.IsNull() {
		if err := m.Resolutions.ElementsAs(ctx, &resolutions, false); err != nil {
			return nil, fmt.Errorf("reading resolutions: %v", err)
		}
	}
	var announceTypes []string
	if !m.AnnounceTypes.IsUnknown() && !m.AnnounceTypes.IsNull() {
		if err := m.AnnounceTypes.ElementsAs(ctx, &announceTypes, false); err != nil {
			return nil, fmt.Errorf("reading announce_types: %v", err)
		}
	}

	return &filter{
		ID:             int(m.ID.ValueInt64()),
		Name:           m.Name.ValueString(),
		Enabled:        m.Enabled.ValueBool(),
		Indexers:       indexers,
		Resolutions:    resolutions,
		AnnounceTypes:  announceTypes,
		ExceptReleases: m.ExceptReleases.ValueString(),
		// NOT NULL columns outside this provider's schema - see client.go's
		// filter struct doc comment.
		Codecs:     []string{},
		Sources:    []string{},
		Containers: []string{},
	}, nil
}

func (r *FilterResource) filterToModel(ctx context.Context, f *filter) (filterResourceModel, error) {
	indexerIDs := make([]int64, len(f.Indexers))
	for i, idx := range f.Indexers {
		indexerIDs[i] = int64(idx.ID)
	}
	indexerIDsList, diags := types.ListValueFrom(ctx, types.Int64Type, indexerIDs)
	if diags.HasError() {
		return filterResourceModel{}, fmt.Errorf("converting indexer_ids: %v", diags)
	}
	resolutionsList, diags := types.ListValueFrom(ctx, types.StringType, f.Resolutions)
	if diags.HasError() {
		return filterResourceModel{}, fmt.Errorf("converting resolutions: %v", diags)
	}
	announceTypesList, diags := types.ListValueFrom(ctx, types.StringType, f.AnnounceTypes)
	if diags.HasError() {
		return filterResourceModel{}, fmt.Errorf("converting announce_types: %v", diags)
	}

	return filterResourceModel{
		ID:             types.Int64Value(int64(f.ID)),
		Name:           types.StringValue(f.Name),
		Enabled:        types.BoolValue(f.Enabled),
		IndexerIDs:     indexerIDsList,
		Resolutions:    resolutionsList,
		AnnounceTypes:  announceTypesList,
		ExceptReleases: types.StringValue(f.ExceptReleases),
	}, nil
}

func (r *FilterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan filterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f, err := r.modelToFilter(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error building filter", err.Error())
		return
	}
	created, err := r.client.createFilter(f)
	if err != nil {
		resp.Diagnostics.AddError("Error creating filter", err.Error())
		return
	}
	// POST /api/filters creates the bare filter row only - confirmed live
	// that indexer associations are silently dropped unless a follow-up
	// PUT is sent (autobrr's Store()/create path doesn't call
	// StoreIndexerConnections, only Update() does). Immediately follow up
	// with a full update using the real created id, then re-fetch to be
	// sure state matches live reality.
	f.ID = created.ID
	if err := r.client.updateFilter(f); err != nil {
		resp.Diagnostics.AddError("Error setting indexer scope on created filter", err.Error())
		return
	}
	created, err = r.client.getFilter(created.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading back created filter", err.Error())
		return
	}

	model, err := r.filterToModel(ctx, created)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding filter", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *FilterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state filterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f, err := r.client.getFilter(int(state.ID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Error reading filter", err.Error())
		return
	}
	if f.ID == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	model, err := r.filterToModel(ctx, f)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding filter", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *FilterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan filterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f, err := r.modelToFilter(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error building filter", err.Error())
		return
	}
	if err := r.client.updateFilter(f); err != nil {
		resp.Diagnostics.AddError("Error updating filter", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *FilterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state filterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteFilter(int(state.ID.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Error deleting filter", err.Error())
		return
	}
}

func (r *FilterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric filter id, got %q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
