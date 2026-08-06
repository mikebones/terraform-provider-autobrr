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

var _ resource.Resource = &IRCNetworkResource{}
var _ resource.ResourceWithImportState = &IRCNetworkResource{}

type IRCNetworkResource struct {
	client *client
}

func NewIRCNetworkResource() resource.Resource {
	return &IRCNetworkResource{}
}

type ircNetworkResourceModel struct {
	ID            types.Int64  `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Server        types.String `tfsdk:"server"`
	Port          types.Int64  `tfsdk:"port"`
	TLS           types.Bool   `tfsdk:"tls"`
	Nick          types.String `tfsdk:"nick"`
	AuthMechanism types.String `tfsdk:"auth_mechanism"`
	AuthAccount   types.String `tfsdk:"auth_account"`
	// AuthPassword is sensitive and NEVER trusted from a GET response
	// (autobrr redacts it to the literal string "<redacted>") - Read()
	// always preserves whatever value is already in state instead of
	// overwriting it from the API. See this resource's Read().
	AuthPassword  types.String `tfsdk:"auth_password"`
	InviteCommand types.String `tfsdk:"invite_command"`
	Channels      types.List   `tfsdk:"channels"`
}

func (r *IRCNetworkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_irc_network"
}

func (r *IRCNetworkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An autobrr IRC network (tracker announce feed).",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
				Description: "Server-assigned network id.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name, e.g. \"BroadcasTheNet\".",
			},
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether autobrr connects to this network.",
			},
			"server": schema.StringAttribute{
				Required:    true,
				Description: "IRC server hostname, e.g. irc.broadcasthe.net",
			},
			"port": schema.Int64Attribute{
				Required: true,
			},
			"tls": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"nick": schema.StringAttribute{
				Required: true,
			},
			"auth_mechanism": schema.StringAttribute{
				Required:    true,
				Description: "One of NONE, SASL_PLAIN, NICKSERV.",
			},
			"auth_account": schema.StringAttribute{
				Optional: true,
				Computed: true,
			},
			"auth_password": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "Never read back from the API (it's redacted on GET) - this resource always preserves the configured value.",
			},
			"invite_command": schema.StringAttribute{
				Optional: true,
				Computed: true,
			},
			"channels": schema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Announce channel names, e.g. [\"#BTN-Announce\"]. All managed channels are enabled.",
			},
		},
	}
}

func (r *IRCNetworkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *IRCNetworkResource) modelToNetwork(ctx context.Context, m ircNetworkResourceModel) (*ircNetwork, error) {
	var channelNames []string
	if err := m.Channels.ElementsAs(ctx, &channelNames, false); err != nil {
		return nil, fmt.Errorf("reading channels list: %v", err)
	}
	channels := make([]ircChannel, len(channelNames))
	for i, name := range channelNames {
		channels[i] = ircChannel{Enabled: true, Name: name}
	}
	return &ircNetwork{
		ID:      m.ID.ValueInt64(),
		Name:    m.Name.ValueString(),
		Enabled: m.Enabled.ValueBool(),
		Server:  m.Server.ValueString(),
		Port:    int(m.Port.ValueInt64()),
		TLS:     m.TLS.ValueBool(),
		Nick:    m.Nick.ValueString(),
		Auth: ircAuth{
			Mechanism: m.AuthMechanism.ValueString(),
			Account:   m.AuthAccount.ValueString(),
			Password:  m.AuthPassword.ValueString(),
		},
		InviteCommand: m.InviteCommand.ValueString(),
		Channels:      channels,
	}, nil
}

// networkToModel updates everything EXCEPT auth_password from the API
// response - prior is the state/plan before this Read/Update, and its
// AuthPassword is carried forward unchanged.
func (r *IRCNetworkResource) networkToModel(ctx context.Context, n *ircNetwork, prior ircNetworkResourceModel) (ircNetworkResourceModel, error) {
	channelNames := make([]string, len(n.Channels))
	for i, ch := range n.Channels {
		channelNames[i] = ch.Name
	}
	channelsList, diags := types.ListValueFrom(ctx, types.StringType, channelNames)
	if diags.HasError() {
		return ircNetworkResourceModel{}, fmt.Errorf("converting channels list: %v", diags)
	}
	return ircNetworkResourceModel{
		ID:            types.Int64Value(n.ID),
		Name:          types.StringValue(n.Name),
		Enabled:       types.BoolValue(n.Enabled),
		Server:        types.StringValue(n.Server),
		Port:          types.Int64Value(int64(n.Port)),
		TLS:           types.BoolValue(n.TLS),
		Nick:          types.StringValue(n.Nick),
		AuthMechanism: types.StringValue(n.Auth.Mechanism),
		AuthAccount:   types.StringValue(n.Auth.Account),
		AuthPassword:  prior.AuthPassword,
		InviteCommand: types.StringValue(n.InviteCommand),
		Channels:      channelsList,
	}, nil
}

func (r *IRCNetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ircNetworkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	n, err := r.modelToNetwork(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error building IRC network", err.Error())
		return
	}
	created, err := r.client.createIRCNetwork(n)
	if err != nil {
		resp.Diagnostics.AddError("Error creating IRC network", err.Error())
		return
	}

	model, err := r.networkToModel(ctx, created, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding IRC network", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *IRCNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ircNetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	n, err := r.client.getIRCNetwork(state.ID.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Error reading IRC network", err.Error())
		return
	}
	if n.ID == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	model, err := r.networkToModel(ctx, n, state)
	if err != nil {
		resp.Diagnostics.AddError("Error decoding IRC network", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}

func (r *IRCNetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ircNetworkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	n, err := r.modelToNetwork(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Error building IRC network", err.Error())
		return
	}
	if err := r.client.updateIRCNetwork(n); err != nil {
		resp.Diagnostics.AddError("Error updating IRC network", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *IRCNetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ircNetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.deleteIRCNetwork(state.ID.ValueInt64()); err != nil {
		resp.Diagnostics.AddError("Error deleting IRC network", err.Error())
		return
	}
}

func (r *IRCNetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected a numeric network id, got %q: %v", req.ID, err))
		return
	}
	// int64 (not string) attribute - set it directly rather than via
	// ImportStatePassthroughID, which passes req.ID through as a raw
	// string and doesn't type-convert.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
