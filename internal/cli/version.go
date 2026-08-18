package cli

// Version is replaced by release builds through -ldflags. Local builds use
// dev so `ki version` remains useful without a release pipeline.
var Version = "dev"
