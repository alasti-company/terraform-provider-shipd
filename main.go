package main

import (
	"context"
	"flag"
	"github.com/alasti-company/terraform-provider-shipd/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"log"
)

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "Enable debugger support")
	flag.Parse()
	if err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{Address: "registry.terraform.io/alasti-company/shipd", Debug: *debug}); err != nil {
		log.Fatal(err)
	}
}
