package domain

// ExecutionHostProvider is one provider as a host's daemon reports it:
// availability, its mode vocabulary, and the models it can launch. This is a
// point-in-time discovery result, never stored — the host's daemon is the only
// authority on what it can run, so AO re-asks rather than remembers.
type ExecutionHostProvider struct {
	Provider    string
	Label       string
	Status      string
	Enabled     bool
	DefaultMode string
	// ModeLabels are display labels, not mode ids: Paseo 0.2.5's provider list
	// reports human labels ("Plan Mode, Bypass") while run --mode takes ids.
	// They are carried for display and never validated against or forwarded.
	ModeLabels []string
	// Modes are (id, label) pairs learned by inspecting a live agent of this
	// provider on the host — the only 0.2.5 surface that returns mode ids.
	// Empty when the host has no agent of this provider yet.
	Modes  []ExecutionProviderMode
	Models []ExecutionProviderModel
}

// ExecutionProviderMode is one launchable mode id with its display label.
type ExecutionProviderMode struct {
	ID    string
	Label string
}

// ExecutionProviderModel is one launchable model with its thinking options.
// ThinkingOptionIDs is the complete valid set for --thinking on this model;
// dispatch refuses any id not in it.
type ExecutionProviderModel struct {
	ID                      string
	Label                   string
	Description             string
	ThinkingOptionIDs       []string
	DefaultThinkingOptionID string
}
