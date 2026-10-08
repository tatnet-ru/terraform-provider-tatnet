package main

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/tatnet-ru/terraform-provider-tatnet/internal/provider"
	"log"
)

var version = "dev"

func main() {
	if err := providerserver.Serve(context.Background(), provider.NewWithVersion(version), providerserver.ServeOpts{Address: "registry.terraform.io/tatnet-ru/tatnet"}); err != nil {
		log.Fatal(err)
	}
}
