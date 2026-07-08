package modules

import "gobot/internal/registry"

func init() {
	if err := registry.RegisterModule(&registry.Module{
		Name: "Utility",
	}); err != nil {
		panic(err)
	}
}
