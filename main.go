package main

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"terraform-provider-autobrr/internal/provider"
)

var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "local/costascomputers/autobrr",
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
