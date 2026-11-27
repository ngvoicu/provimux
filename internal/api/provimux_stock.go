package api

// provimuxStockOptions reports whether a server is built as the gateway ships it, for the record of what answers said
// (.github/provimux/design/answered-by.md, the lead's amendment of 2026-11-27 (2), rule B.1): with none of the SDK's
// options that put another program's code where a response or a WebSocket connection is written (middleware, an engine
// or a router configurator). The gateway as shipped passes none of them.
func provimuxStockOptions(options *serverOptionConfig) bool {
	return options != nil && len(options.extraMiddleware) == 0 && options.engineConfigurator == nil && options.routerConfigurator == nil
}
