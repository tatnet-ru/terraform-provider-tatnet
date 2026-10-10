package provider

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/tatnet-ru/tatnet-go/tatnet"
)

type Provider struct{ version string }
type providerModel struct {
	APIKey   types.String `tfsdk:"api_key"`
	Endpoint types.String `tfsdk:"endpoint"`
}

func New() provider.Provider { return &Provider{version: "dev"} }

func NewWithVersion(version string) func() provider.Provider {
	return func() provider.Provider { return &Provider{version: version} }
}
func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "tatnet"
	resp.Version = p.version
}
func (*Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"api_key":  schema.StringAttribute{Optional: true, Sensitive: true, Description: "Account API key. Defaults to TATNET_API_KEY; requires vm:read for the project."},
		"endpoint": schema.StringAttribute{Optional: true, Description: "HTTPS API base URL, including /v1. Defaults to https://api.tatnet.ru/v1."},
	}}
}
func (*Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.APIKey.IsUnknown() || config.Endpoint.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "API key and endpoint must be known before reading images.")
		return
	}
	key := os.Getenv("TATNET_API_KEY")
	if !config.APIKey.IsNull() {
		key = config.APIKey.ValueString()
	}
	if strings.TrimSpace(key) == "" {
		resp.Diagnostics.AddError("Missing API key", "Set TATNET_API_KEY or api_key.")
		return
	}
	endpoint := tatnet.DefaultBaseURL
	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		resp.Diagnostics.AddError("Invalid API endpoint", "Use an HTTPS base URL without credentials, query, or fragment.")
		return
	}
	client, err := tatnet.NewClientWithResponses(endpoint, tatnet.WithAPIKey(key), tatnet.WithHTTPClient(&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}))
	if err != nil {
		resp.Diagnostics.AddError("Cannot configure TatNet client", "Check the API endpoint.")
		return
	}
	resp.DataSourceData = client
	resp.ResourceData = client
}
func (*Provider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewVMResource, NewFloatingIPResource, NewVPCResource, NewNATGatewayResource, NewDNSRecordResource}
}
func (*Provider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewImageDataSource, NewVPCDataSource, NewDNSZoneDataSource, NewDNSRecordDataSource}
}
