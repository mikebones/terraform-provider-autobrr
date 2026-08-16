package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &AutobrrProvider{}

type AutobrrProvider struct {
	version string
}

type autobrrProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIToken types.String `tfsdk:"api_token"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &AutobrrProvider{version: version}
	}
}

func (p *AutobrrProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "autobrr"
	resp.Version = p.version
}

func (p *AutobrrProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages autobrr's IRC networks, indexers, feeds, download clients, actions, and filters (a narrow, structural subset - see this provider's README) against its REST API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Required:    true,
				Description: "Base URL of the autobrr instance, e.g. https://autobrr.costascomputers.com",
			},
			"api_token": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "autobrr API key (Settings > API Keys in its UI). Bootstrapping the very first key still requires a one-time manual login - see this provider's README.",
			},
		},
	}
}

func (p *AutobrrProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data autobrrProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	c := newClient(data.Endpoint.ValueString(), data.APIToken.ValueString())
	resp.ResourceData = c
}

func (p *AutobrrProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewIRCNetworkResource,
		NewIndexerResource,
		NewFeedResource,
		NewDownloadClientResource,
		NewActionResource,
		NewFilterResource,
	}
}

func (p *AutobrrProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
