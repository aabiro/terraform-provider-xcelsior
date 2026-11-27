package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/aabiro/terraform-provider-xcelsior/internal/provider"
)

// Generate the registry docs from the provider schema and examples/.
//go:generate go tool tfplugindocs generate --provider-name xcelsior

// version is set by the release build (goreleaser ldflags).
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/aabiro/xcelsior",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
