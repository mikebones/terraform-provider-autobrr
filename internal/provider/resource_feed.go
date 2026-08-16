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

var _ resource.Resource = &FeedResource{}
var _ resource.ResourceWithImportState = &FeedResource{}

type FeedResource struct {
	client *client
}

func NewFeedResource() resource.Resource {
	return &FeedResource{}
}

// feedResourceModel intentionally omits autobrr's nested "indexer" display
// object - see client.go's feed struct doc comment. indexer_id (an
// autobrr_indexer.id) is the only thing that actually links a feed to its
// indexer; the nested object is a read-only join the server fills in and
// this provider never round-trips it.
type feedResourceModel struct {
	ID           types.Int64  `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	IndexerID    types.Int64  `tfsdk:"indexer_id"`
	Type         types.String `tfsdk:"type"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	URL          types.String `tfsdk:"url"`
	Interval     types.Int64  `tfsdk:"interval"`
	Timeout      types.Int64  `tfsdk:"timeout"`
	MaxAge       types.Int64  `tfsdk:"max_age"`
	ApiKey       types.String `tfsdk:"api_key"`
	Cookie       types.String `tfsdk:"cookie"`
	DownloadType types.String `tfsdk:"download_type"`
}

func (r *FeedResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_feed"
}

func (r *FeedResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An autobrr feed (RSS/Torznab/Newznab poller), always paired with an autobrr_indexer " +
			"via indexer_id. CRITICAL (confirmed live 2026-08-16): autobrr's FeedRepo.Store only ever reads " +
			"the flat indexer_id field on write - there is no nested \"indexer\" object in this schema " +
			"because sending one does nothing; omitting indexer_id is what silently drops the feed (POST " +
			"returns 201 with a populated-looking body, but it never persists a real indexer link and " +
			"disappears on the next read).",
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
			"indexer_id": schema.Int64Attribute{
				Required:    true,
				Description: "The autobrr_indexer this feed belongs to. This is the ONLY field that links a feed to its indexer - see the resource description.",
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "RSS, TORZNAB, or NEWZNAB.",
			},
			"enabled": schema.BoolAttribute{
				Required: true,
			},
			"url": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "For RSS trackers this is typically the whole story - auth (passkey/authkey/user) travels as query params baked directly into the URL, not as separate credentials. Not redacted by autobrr's API on read (unlike IRC auth_password / download-client api_key), but still marked sensitive here for state/plan hygiene.",
			},
			"interval": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Seconds between polls.",
			},
			"timeout": schema.Int64Attribute{
				Optional: true,
				Computed: true,
			},
			"max_age": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Seconds. NOTE: autobrr's own create path (FeedRepo.Store's INSERT column list) silently drops this on creation - only Update's column list includes it. This resource's Create() works around that by immediately following up with an Update() after every create (see Create()), so it still ends up set correctly; a bare POST outside this provider would not.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Computed:  true,
				Sensitive: true,
			},
			"cookie": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Sensitive:   true,
				Description: "Same create-path gap as max_age - see that field's description.",
			},
			"download_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "MAGNET or TORRENT (autobrr's settings.download_type). Flattened out of autobrr's nested settings object since it's the only field in there that matters operationally.",
			},
		},
	}
}

func (r *FeedResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *FeedResource) modelToFeed(m feedResourceModel) *feed {
	f := &feed{
		ID:        int(m.ID.ValueInt64()),
		Name:      m.Name.ValueString(),
		IndexerID: int(m.IndexerID.ValueInt64()),
		Type:      m.Type.ValueString(),
		Enabled:   m.Enabled.ValueBool(),
		URL:       m.URL.ValueString(),
		Interval:  int(m.Interval.ValueInt64()),
		Timeout:   int(m.Timeout.ValueInt64()),
		MaxAge:    int(m.MaxAge.ValueInt64()),
		ApiKey:    m.ApiKey.ValueString(),
		Cookie:    m.Cookie.ValueString(),
	}
	if !m.DownloadType.IsUnknown() && !m.DownloadType.IsNull() {
		f.Settings = &feedSettings{DownloadType: m.DownloadType.ValueString()}
	}
	return f
}

// feedToModel: GET /api/feeds (list) responses never populate the flat
// indexer_id field at all (confirmed live: it's simply absent from the
// JSON, not just zero-valued) - only the nested, read-only "indexer"
// join object carries it there. This is the mirror image of the
// write-side gotcha this resource exists to work around: on WRITE only
// indexer_id matters and the nested object does nothing; on READ only
// the nested object is populated and indexer_id decodes as 0. Recover
// the real value from f.Indexer.ID whenever the flat field comes back
// empty, so a plain `terraform plan` after import/refresh doesn't show a
// bogus indexer_id 0 -> N diff on every run.
func (r *FeedResource) feedToModel(f *feed) feedResourceModel {
	indexerID := f.IndexerID
	if indexerID == 0 && f.Indexer != nil {
		indexerID = f.Indexer.ID
	}
	m := feedResourceModel{
		ID:        types.Int64Value(int64(f.ID)),
		Name:      types.StringValue(f.Name),
		IndexerID: types.Int64Value(int64(indexerID)),
		Type:      types.StringValue(f.Type),
		Enabled:   types.BoolValue(f.Enabled),
		URL:       types.StringValue(f.URL),
		Interval:  types.Int64Value(int64(f.Interval)),
		Timeout:   types.Int64Value(int64(f.Timeout)),
		MaxAge:    types.Int64Value(int64(f.MaxAge)),
		ApiKey:    types.StringValue(f.ApiKey),
		Cookie:    types.StringValue(f.Cookie),
	}
	if f.Settings != nil {
		m.DownloadType = types.StringValue(f.Settings.DownloadType)
	} else {
		m.DownloadType = types.StringValue("")
	}
	return m
}

// Create works around FeedRepo.Store's incomplete INSERT column list (see
// this resource's Schema description and client.go's createFeed comment):
// create the bare row, then immediately update it with the full desired
// state using the real assigned id, then re-fetch to confirm state
// matches live reality - the same "create is lossy, fix it up with an
// immediate follow-up write" shape resource_filter.go already uses for
// its own create-path gap (indexer associations there, cookie/max_age
// here - different fields, same root cause pattern in this codebase).
func (r *FeedResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan feedResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := r.modelToFeed(plan)
	created, err := r.client.createFeed(f)
	if err != nil {
		resp.Diagnostics.AddError("Error creating feed", err.Error())
		return
	}

	f.ID = created.ID
	if err := r.client.updateFeed(f); err != nil {
		resp.Diagnostics.AddError("Error setting cookie/max_age on created feed", err.Error())
		return
	}
	final, err := r.client.getFeed(f.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading back created feed", err.Error())
		return
	}
	if final == nil {
		resp.Diagnostics.AddError("Error reading back created feed", "feed disappeared immediately after create")
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.feedToModel(final))...)
}

func (r *FeedResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state feedResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f, err := r.client.getFeed(int(state.ID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Error reading feed", err.Error())
		return
	}
	if f == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.feedToModel(f))...)
}

func (r *FeedResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan feedResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := r.modelToFeed(plan)
	if err := r.client.updateFeed(f); err != nil {
		resp.Diagnostics.AddError("Error updating feed", err.Error())
		return
	}

	updated, err := r.client.getFeed(f.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading back updated feed", err.Error())
		return
	}
	if updated == nil {
		resp.Diagnostics.AddError("Error reading back updated feed", "feed disappeared immediately after update")
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.feedToModel(updated))...)
}

func (r *FeedResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state feedResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteFeed(int(state.ID.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Error deleting feed", err.Error())
		return
	}
}

func (r *FeedResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric feed id, got %q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
