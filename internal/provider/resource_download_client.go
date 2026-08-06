package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &DownloadClientResource{}
var _ resource.ResourceWithImportState = &DownloadClientResource{}

type DownloadClientResource struct {
	client *client
}

func NewDownloadClientResource() resource.Resource {
	return &DownloadClientResource{}
}

type downloadClientResourceModel struct {
	ID      types.Int64  `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	Enabled types.Bool   `tfsdk:"enabled"`
	Host    types.String `tfsdk:"host"`
	Port    types.Int64  `tfsdk:"port"`
	TLS     types.Bool   `tfsdk:"tls"`
	// APIKey is sensitive and never trusted from a GET response (redacted
	// to "<redacted>") - same preserve-on-read rule as the IRC network's
	// auth_password.
	APIKey types.String `tfsdk:"api_key"`
}

func (r *DownloadClientResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_download_client"
}

func (r *DownloadClientResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An autobrr download client (includes *arr push targets like SONARR/RADARR, not just torrent clients).",
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
			"type": schema.StringAttribute{
				Required:    true,
				Description: "SONARR, RADARR, TRANSMISSION, DELUGE_V2, etc. - see autobrr's DownloadClientType.",
			},
			"enabled": schema.BoolAttribute{
				Required: true,
			},
			"host": schema.StringAttribute{
				Required:    true,
				Description: "For *arr types this is the full base URL (e.g. http://sonarr.sonarr.svc.cluster.local:8989), not just a hostname.",
			},
			"port": schema.Int64Attribute{
				Optional: true,
				Computed: true,
			},
			"tls": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "*arr API key (settings.apikey). Never read back from the API - this resource always preserves the configured value.",
			},
		},
	}
}

func (r *DownloadClientResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DownloadClientResource) modelToClient(m downloadClientResourceModel) *downloadClient {
	return &downloadClient{
		ID:      int32(m.ID.ValueInt64()),
		Name:    m.Name.ValueString(),
		Type:    m.Type.ValueString(),
		Enabled: m.Enabled.ValueBool(),
		Host:    m.Host.ValueString(),
		Port:    int(m.Port.ValueInt64()),
		TLS:     m.TLS.ValueBool(),
		Settings: downloadClientSettings{
			APIKey: m.APIKey.ValueString(),
		},
	}
}

func (r *DownloadClientResource) clientToModel(d *downloadClient, prior downloadClientResourceModel) downloadClientResourceModel {
	return downloadClientResourceModel{
		ID:      types.Int64Value(int64(d.ID)),
		Name:    types.StringValue(d.Name),
		Type:    types.StringValue(d.Type),
		Enabled: types.BoolValue(d.Enabled),
		Host:    types.StringValue(d.Host),
		Port:    types.Int64Value(int64(d.Port)),
		TLS:     types.BoolValue(d.TLS),
		APIKey:  prior.APIKey,
	}
}

func (r *DownloadClientResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan downloadClientResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.createDownloadClient(r.modelToClient(plan))
	if err != nil {
		resp.Diagnostics.AddError("Error creating download client", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.clientToModel(created, plan))...)
}

func (r *DownloadClientResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state downloadClientResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	d, err := r.client.getDownloadClient(int32(state.ID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Error reading download client", err.Error())
		return
	}
	if d.ID == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, r.clientToModel(d, state))...)
}

func (r *DownloadClientResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan downloadClientResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.updateDownloadClient(r.modelToClient(plan)); err != nil {
		resp.Diagnostics.AddError("Error updating download client", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *DownloadClientResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state downloadClientResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteDownloadClient(int32(state.ID.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Error deleting download client", err.Error())
		return
	}
}

func (r *DownloadClientResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric client id, got %q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
