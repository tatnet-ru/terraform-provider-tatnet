package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestProtocolSchema(t *testing.T) {
	server := providerserver.NewProtocol6(New())()
	response, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range response.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatal(d.Detail)
		}
	}
	if response.DataSourceSchemas["tatnet_image"] == nil {
		t.Fatal("image data source not registered")
	}
	if response.ResourceSchemas["tatnet_vm"] == nil {
		t.Fatal("VM resource not registered")
	}
}

func TestProviderConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		key, endpoint interface{}
		env           string
		wantError     bool
	}{
		{"environment key", nil, nil, "env-key", false},
		{"explicit key", "config-key", nil, "", false},
		{"empty explicit key overrides environment", "", nil, "env-key", true},
		{"missing key", nil, nil, "", true},
		{"unknown key", tftypes.UnknownValue, nil, "env-key", true},
		{"unknown endpoint", "key", tftypes.UnknownValue, "", true},
		{"insecure endpoint", "key", "http://example.com/v1", "", true},
		{"embedded credentials", "key", "https://user:secret@example.com/v1", "", true},
		{"query", "key", "https://example.com/v1?token=secret", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TATNET_API_KEY", tc.env)
			ctx := context.Background()
			p := New()
			var sr provider.SchemaResponse
			p.Schema(ctx, provider.SchemaRequest{}, &sr)
			raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
				"api_key": tftypes.NewValue(tftypes.String, tc.key), "endpoint": tftypes.NewValue(tftypes.String, tc.endpoint),
			})
			var resp provider.ConfigureResponse
			p.Configure(ctx, provider.ConfigureRequest{Config: tfsdk.Config{Raw: raw, Schema: sr.Schema}}, &resp)
			if resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
			if !tc.wantError && resp.DataSourceData == nil {
				t.Fatal("missing client")
			}
		})
	}
}

func TestReleaseVersion(t *testing.T) {
	var resp provider.MetadataResponse
	NewWithVersion("0.1.0")().Metadata(context.Background(), provider.MetadataRequest{}, &resp)
	if resp.Version != "0.1.0" {
		t.Fatalf("incorrect release version: %q", resp.Version)
	}
}
